package tenant

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Options configures the plugin. Zero values are usable: the default handler
// reads frameworkcontext and the column is tenant_id.
// Options 配置插件。零值即可用：默认 Handler 读 frameworkcontext，列名为 tenant_id。
type Options struct {
	// Handler supplies the tenant ID and column name. Nil uses ContextHandler.
	// Handler 提供租户 ID 与列名；为 nil 时使用 ContextHandler。
	Handler Handler

	// OverwriteExplicit replaces an explicitly assigned tenant column on INSERT.
	// With the default false, a caller that already filled the column keeps its
	// value, which allows an administrator to write into another tenant on
	// purpose. Set true to force every insert to the context tenant.
	// OverwriteExplicit 为 true 时覆盖 INSERT 上已显式赋值的租户列。默认 false，
	// 保留调用方填好的值，让管理端可以有意写入别的租户；设 true 则强制使用上下文租户。
	OverwriteExplicit bool

	// OnBypass is called when Without is in effect for a tenant-aware statement.
	// It exists so privileged bypasses leave an audit trail. May be nil.
	// OnBypass 在 Without 对租户感知语句生效时调用，让特权绕过留痕。可为 nil。
	OnBypass func(ctx context.Context, table string)

	// OnUnresolved is called when GORM cannot parse a schema (Table("x") with
	// no model, raw SQL). It mirrors swoolefy's warnNotRewritten hook.
	// OnUnresolved 在 GORM 无法解析 schema 时（无 model 的 Table("x")、原生 SQL）调用，
	// 对应 swoolefy 的 warnNotRewritten 告警钩子。可为 nil。
	OnUnresolved func(ctx context.Context, table string)
}

// Plugin implements gorm.Plugin. It holds only immutable configuration, so one
// instance can be shared by all statements and goroutines.
// Plugin 实现 gorm.Plugin。它只持有不可变配置，因此一个实例可被所有语句与 goroutine 共享。
type Plugin struct {
	handler           Handler
	column            string
	overwriteExplicit bool
	onBypass          func(context.Context, string)
	onUnresolved      func(context.Context, string)
}

// NewPlugin builds a plugin. A nil Handler falls back to ContextHandler.
// NewPlugin 构造插件；Handler 为 nil 时回退到 ContextHandler。
func NewPlugin(opts Options) *Plugin {
	h := opts.Handler
	if h == nil {
		h = ContextHandler{}
	}
	return &Plugin{
		handler:           h,
		column:            strings.TrimSpace(h.Column()),
		overwriteExplicit: opts.OverwriteExplicit,
		onBypass:          opts.OnBypass,
		onUnresolved:      opts.OnUnresolved,
	}
}

// Name implements gorm.Plugin. A fixed name makes a second db.Use on the same
// *gorm.DB fail with GORM's duplicate-callback error.
func (p *Plugin) Name() string { return "gofy:tenant" }

// Initialize implements gorm.Plugin. Only the four statement processors that
// GORM builds from a model are hooked; Raw/Row are deliberately left alone
// because they have no structured schema to decide on.
// Initialize 实现 gorm.Plugin。只挂 GORM 依据 model 构造的四个 processor；Raw/Row
// 刻意不挂，因为它们没有可判定的结构化 schema。
func (p *Plugin) Initialize(db *gorm.DB) error {
	if db == nil || p == nil {
		return nil
	}
	type registration struct {
		before callbackRegistrar
		name   string
		fn     func(*gorm.DB)
	}
	filter := p.filter
	fill := p.fill
	registrations := []registration{
		{db.Callback().Query().Before("gorm:query"), "query", filter},
		{db.Callback().Update().Before("gorm:update"), "update", filter},
		{db.Callback().Delete().Before("gorm:delete"), "delete", filter},
		{db.Callback().Create().Before("gorm:create"), "create", fill},
	}
	for _, item := range registrations {
		if err := item.before.Register("gofy:tenant:before_"+item.name, item.fn); err != nil {
			return err
		}
	}
	return nil
}

// callbackRegistrar is the exported part of GORM's unexported callback type.
// callbackRegistrar 是 GORM 未导出回调类型中已导出的部分。
type callbackRegistrar interface {
	Register(name string, fn func(*gorm.DB)) error
}

// Instrument installs the plugin on db. It is the counterpart of
// otel.InstrumentGORM and should be called after it, so the tenant handler
// reads the final Statement.Context (the tracing plugin replaces it).
// Instrument 在 db 上安装插件，对应 otel.InstrumentGORM。应在 otel 之后调用，
// 这样租户 Handler 读到的是最终的 Statement.Context（链路插件会替换它）。
func Instrument(db *gorm.DB, opts Options) error {
	if db == nil {
		return ErrPluginNil
	}
	return db.Use(NewPlugin(opts))
}

// resolve reports the tenant ID for a statement, together with whether the
// statement is tenant-aware at all. It returns ok=false when the statement
// should be left untouched (bypass, or a table without the column).
// resolve 返回语句的租户 ID 以及该语句是否需要租户隔离。ok=false 表示保持原样
// （被绕过，或表不含租户列）。
func (p *Plugin) resolve(db *gorm.DB) (string, bool) {
	stmt := db.Statement
	if stmt == nil {
		return "", false
	}
	ctx := stmt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	// Bypass is checked first, before any schema decision: Without must win on
	// every statement shape, including Table("x") with no parseable model.
	// 绕过最先检查，先于任何 schema 判定：Without 必须在所有语句形态上生效，
	// 包括 Table("x") 这种解析不出 model 的情况。
	if isBypassed(ctx) {
		if p.onBypass != nil {
			p.onBypass(ctx, stmt.Table)
		}
		return "", false
	}

	// GORM could not parse a model. Falling back to IgnoreTable keeps the
	// decision with the application; the default (false) stays fail-closed.
	// GORM 没能解析出 model，交给 IgnoreTable 决定；默认 false 保持失败关闭。
	if stmt.Schema == nil {
		if p.handler.IgnoreTable(stmt.Table) {
			return "", false
		}
		if p.onUnresolved != nil {
			p.onUnresolved(ctx, stmt.Table)
		}
		return p.tenantID(ctx, stmt.Table, db), true
	}

	// LookUpField matches both the Go field name and the DB column name.
	// LookUpField 同时匹配 Go 字段名与数据库列名。
	if stmt.Schema.LookUpField(p.column) == nil {
		return "", false
	}
	return p.tenantID(ctx, stmt.Table, db), true
}

func (p *Plugin) tenantID(ctx context.Context, table string, db *gorm.DB) string {
	id, ok := overrideTenant(ctx)
	if !ok {
		id = p.handler.TenantID(ctx)
	}
	if strings.TrimSpace(id) == "" {
		// Fail closed: adding an error short-circuits GORM's SQL builder, so no
		// unfiltered statement ever reaches the database.
		// 失败关闭：AddError 会短路 GORM 的 SQL 构造，不带过滤的语句不会到达数据库。
		db.AddError(fmt.Errorf("%w: table %s", ErrTenantRequired, table))
		return ""
	}
	return id
}

// filter appends the tenant condition to SELECT / UPDATE / DELETE.
// filter 给 SELECT / UPDATE / DELETE 追加租户条件。
func (p *Plugin) filter(db *gorm.DB) {
	if db == nil || db.Error != nil || db.Statement == nil {
		return
	}
	id, ok := p.resolve(db)
	if !ok || db.Error != nil {
		return
	}
	stmt := db.Statement
	if hasTenantCondition(stmt, p.column) {
		return
	}
	stmt.AddClause(clause.Where{Exprs: []clause.Expression{
		clause.Eq{Column: clause.Column{Name: p.column}, Value: id},
	}})
}

// fill sets the tenant column on INSERT when the caller did not.
// fill 在 INSERT 时填充调用方未赋值的租户列。
func (p *Plugin) fill(db *gorm.DB) {
	if db == nil || db.Error != nil || db.Statement == nil {
		return
	}
	id, ok := p.resolve(db)
	if !ok || db.Error != nil {
		return
	}
	stmt := db.Statement
	if !p.overwriteExplicit && explicitColumnSet(stmt, p.column) {
		return
	}
	stmt.SetColumn(p.column, id, true)
}

// hasTenantCondition reports whether the WHERE clause already constrains the
// tenant column. It walks the structured expressions, so it is exact rather
// than a regexp guess. Nested AndConditions/OrConditions are covered too.
// hasTenantCondition 报告 WHERE 中是否已有租户列条件。遍历结构化表达式，因此是精确
// 判断而非正则猜测；嵌套的 AndConditions/OrConditions 也覆盖。
func hasTenantCondition(stmt *gorm.Statement, column string) bool {
	c, ok := stmt.Clauses["WHERE"]
	if !ok {
		return false
	}
	return exprsMentionColumn([]clause.Expression{c.Expression}, column)
}

func exprsMentionColumn(exprs []clause.Expression, column string) bool {
	for _, expr := range exprs {
		switch v := expr.(type) {
		case nil:
		case clause.Eq:
			if columnName(v.Column) == column {
				return true
			}
		case clause.AndConditions:
			if exprsMentionColumn(v.Exprs, column) {
				return true
			}
		case clause.OrConditions:
			if exprsMentionColumn(v.Exprs, column) {
				return true
			}
		case clause.NotConditions:
			if exprsMentionColumn(v.Exprs, column) {
				return true
			}
		case clause.Where:
			if exprsMentionColumn(v.Exprs, column) {
				return true
			}
		case clause.Expr:
			// Raw SQL condition: fall back to a conservative substring check
			// so a hand-written "tenant_id = ?" is still recognised.
			// 原生 SQL 条件：保守地做子串检查，让手写的 "tenant_id = ?" 也能识别。
			if strings.Contains(strings.ToLower(v.SQL), strings.ToLower(column)) {
				return true
			}
		}
	}
	return false
}

func columnName(v any) string {
	switch c := v.(type) {
	case clause.Column:
		return c.Name
	case string:
		return c
	}
	return ""
}

// explicitColumnSet reports whether Dest already carries a non-zero tenant
// column. It handles struct, pointer-to-struct, []struct, and map Dest shapes
// that GORM's Create accepts.
// explicitColumnSet 报告 Dest 上是否已有非零的租户列取值。覆盖 GORM Create 接受的
// struct、指针、切片与 map 形态。
func explicitColumnSet(stmt *gorm.Statement, column string) bool {
	if stmt.Dest == nil {
		return false
	}
	switch dest := stmt.Dest.(type) {
	case map[string]any:
		v, exists := dest[column]
		return exists && !isZeroValue(v)
	case []map[string]any:
		for _, m := range dest {
			if v, exists := m[column]; exists && !isZeroValue(v) {
				return true
			}
		}
		return false
	}

	field := stmt.Schema.LookUpField(column)
	if field == nil || field.ValueOf == nil {
		return false
	}
	rv := reflect.ValueOf(stmt.Dest)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return false
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct:
		_, zero := field.ValueOf(stmt.Context, rv)
		return !zero
	case reflect.Slice, reflect.Array:
		// Batch insert: honour an explicit value if any row has one. GORM's
		// SetColumn(fromCallbacks=true) then fills the remaining rows.
		// 批量插入：任一行显式赋值即视为已提供。
		for i := 0; i < rv.Len(); i++ {
			_, zero := field.ValueOf(stmt.Context, rv.Index(i))
			if !zero {
				return true
			}
		}
	}
	return false
}

func isZeroValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.IsZero()
}
