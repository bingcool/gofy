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
	update := p.filterUpdate
	fill := p.fill
	registrations := []registration{
		{db.Callback().Query().Before("gorm:query"), "query", filter},
		{db.Callback().Update().Before("gorm:update"), "update", update},
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

// filter adds the tenant condition to SELECT / UPDATE / DELETE.
//
// The condition is always ANDed with the caller's whole WHERE. When a WHERE is
// already present it is wrapped in one AndConditions group first: GORM appends
// a new expression to the end of the list, and `a OR b` plus `AND tenant` would
// render as `a OR b AND tenant` — restricting the last operand, not the query.
// filter 给 SELECT / UPDATE / DELETE 追加租户条件。
//
// 条件总是与调用方的整个 WHERE 用 AND 组合。已有 WHERE 时先把它包成一个
// AndConditions 分组：GORM 会把新表达式追加到列表末尾，`a OR b` 加上 `AND tenant`
// 会渲染成 `a OR b AND tenant`——限制的是最后一个操作数，而不是整个查询。
func (p *Plugin) filter(db *gorm.DB) { p.scope(db, false) }

// filterUpdate is filter plus the write-side guard: the tenant column never
// appears in a SET list.
//
// GORM's Save, and Select("*").Updates, write every field of the destination, so
// the most common update shape — a struct carrying the primary key and the
// fields being changed — sent a zero tenant_id along with it. The row survived
// but left every tenant: it is invisible to each tenant-scoped query, and
// nothing reported an error. Omitting the column keeps the value the row already
// has. There is nothing to write anyway: the WHERE clause pins the row to the
// context tenant, and a caller that really wants to move a row between tenants
// uses tenant.Without, which skips this guard entirely.
// filterUpdate 是 filter 加上写入侧保护：租户列永远不出现在 SET 列表里。
//
// GORM 的 Save 与 Select("*").Updates 会写目标的所有字段，于是最常见的更新写法——一个
// 只带主键和要改字段的结构体——把零值 tenant_id 一起发了出去。行还在，却离开了所有租户：
// 任何带租户过滤的查询都看不到它，而且没有任何报错。忽略该列即保留行原有的值。本来也没有
// 什么要写：WHERE 已经把行限定在上下文租户内；确实要在租户之间搬行时用 tenant.Without，
// 那条路径完全跳过这个守卫。
func (p *Plugin) filterUpdate(db *gorm.DB) { p.scope(db, true) }

func (p *Plugin) scope(db *gorm.DB, protectSet bool) {
	if db == nil || db.Error != nil || db.Statement == nil {
		return
	}
	id, ok := p.resolve(db)
	if !ok || db.Error != nil {
		return
	}
	stmt := db.Statement
	if protectSet {
		// Omits is applied after Selects, so this also wins over the "*" that
		// Save installs.
		// Omits 在 Selects 之后生效，因此也能压过 Save 装上的 "*"。
		stmt.Omits = append(stmt.Omits, p.column)
	}
	scope := clause.Eq{Column: clause.Column{Name: p.column}, Value: id}

	where, exists := stmt.Clauses["WHERE"]
	existing, isWhere := where.Expression.(clause.Where)
	if !exists || !isWhere || len(existing.Exprs) == 0 {
		stmt.AddClause(clause.Where{Exprs: []clause.Expression{scope}})
		return
	}
	if exprsConstrainColumn(existing.Exprs, p.column) {
		return
	}
	where.Expression = clause.Where{Exprs: []clause.Expression{
		clause.AndConditions{Exprs: existing.Exprs},
		scope,
	}}
	stmt.Clauses["WHERE"] = where
}

// fill sets the tenant column on INSERT where the caller left it empty.
//
// The old "any explicit row means the batch is explicit" rule was wrong twice
// over: it skipped the whole batch, and GORM builds INSERT values by reading
// each row's field, so a batch-wide SetColumn would have overwritten the rows
// that did carry a value. Fill is now per row — explicit values are kept, zero
// ones get the context tenant — for struct slices and map slices alike.
// fill 给 INSERT 中调用方留空的租户列赋值。
//
// 原先"任一行显式赋值就视为整批已提供"的规则两处都错：它直接跳过整批，而 GORM 是按行
// 读取字段构造 INSERT 的，整批 SetColumn 反而会覆盖本来有值的行。现在按行填——显式值保留，
// 零值取上下文租户——结构体切片与 map 切片都覆盖。
func (p *Plugin) fill(db *gorm.DB) {
	if db == nil || db.Error != nil || db.Statement == nil {
		return
	}
	id, ok := p.resolve(db)
	if !ok || db.Error != nil {
		return
	}
	stmt := db.Statement
	if p.overwriteExplicit {
		stmt.SetColumn(p.column, id, true)
		return
	}

	// maps are handled by key, including a pointer to a map or a slice of maps.
	// map 按键赋值，同时兼容指向 map 或 map 切片的指针。
	dest := stmt.Dest
	for {
		switch v := dest.(type) {
		case *map[string]any:
			dest = *v
			continue
		case *[]map[string]any:
			dest = *v
			continue
		}
		break
	}
	switch v := dest.(type) {
	case map[string]any:
		if value, exists := v[p.column]; !exists || isZeroValue(value) {
			v[p.column] = id
		}
		return
	case []map[string]any:
		for _, row := range v {
			if value, exists := row[p.column]; !exists || isZeroValue(value) {
				row[p.column] = id
			}
		}
		return
	}

	field := stmt.Schema.LookUpField(p.column)
	if field == nil || field.ValueOf == nil {
		return
	}
	rv := reflect.ValueOf(stmt.Dest)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct:
		if _, zero := field.ValueOf(stmt.Context, rv); zero {
			stmt.SetColumn(p.column, id, true)
		}
	case reflect.Slice, reflect.Array:
		// GORM reads each row's field when it builds the INSERT values, so the
		// zero rows must be set on the caller's slice itself.
		// GORM 构造 INSERT 值时逐行读字段，因此必须直接改调用方切片里的零值行。
		for i := 0; i < rv.Len(); i++ {
			if _, zero := field.ValueOf(stmt.Context, rv.Index(i)); zero {
				if err := field.Set(stmt.Context, rv.Index(i), id); err != nil {
					stmt.AddError(err)
				}
			}
		}
	}
}

// constrainsColumn walks the expression tree along the paths that restrict the
// result set: the top-level AND chain, AndConditions groups and nested Where.
// OrConditions and NotConditions are deliberately not descended into — a tenant
// column inside them does not scope the statement.
//
// Only an equality counts, and only when it is ANDed with the rest of the
// condition. A disjunction or a negation that happens to mention the column
// restricts nothing, and treating it as "already filtered" is exactly what let
// `Where("tenant_id <> ?")` and `Where("tenant_id = ? OR is_public = 1")` read
// across tenants. Raw SQL gets the same treatment: a lone `tenant_id = ?`
// comparison is recognised, anything else is not. A caller that really means
// another tenant uses tenant.Without.
// constrainsColumn 沿真正限制结果集的路径遍历表达式树：顶层 AND 链、AndConditions
// 分组与嵌套 Where。刻意不进入 OrConditions 与 NotConditions——里面的租户列并不
// 收窄语句范围。
//
// 只有等值才算，且必须是与其余条件 AND 在一起的那一个。字面上提到该列的析取或取反
// 并不限制结果集，把它们当成"已过滤"正是 `Where("tenant_id <> ?")`、
// `Where("tenant_id = ? OR is_public = 1")` 能跨租户读取的原因。手写 SQL 同样处理：
// 只认单独一条 `tenant_id = ?` 比较，其它一律不认。确实要操作别的租户时请用
// tenant.Without。
func constrainsColumn(expr clause.Expression, column string) bool {
	switch v := expr.(type) {
	case nil:
		return false
	case clause.Eq:
		return columnName(v.Column) == column
	case clause.AndConditions:
		for _, inner := range v.Exprs {
			if constrainsColumn(inner, column) {
				return true
			}
		}
	case clause.Where:
		return exprsConstrainColumn(v.Exprs, column)
	case clause.Expr:
		return isTenantEquality(v.SQL, column)
	}
	return false
}

// exprsConstrainColumn walks one conjunction list from the end.
//
// GORM keeps the expressions in source order and renders an OrConditions as
// "OR ...", so whatever precedes the last OrConditions is the left operand of a
// disjunction — `Where("tenant_id = ?").Or("code = ?")` builds exactly that, and
// the tenant equality there does not scope the query. Only expressions after it
// are ANDed with the whole condition, so the scan stops at the first
// OrConditions from the right.
// exprsConstrainColumn 从末尾遍历一组合取表达式。
//
// GORM 按书写顺序保存表达式，并把 OrConditions 渲染成 "OR ..."，因此最后一个
// OrConditions 之前的内容都是析取的左操作数——`Where("tenant_id = ?").Or("code = ?")`
// 构造的正是这种形态，其中的租户等值并不收窄查询。只有它之后（索引更大）的表达式才是与
// 整体 AND 在一起的，所以扫描在从右往左遇到的第一个 OrConditions 处停止。
func exprsConstrainColumn(exprs []clause.Expression, column string) bool {
	for i := len(exprs) - 1; i >= 0; i-- {
		if _, ok := exprs[i].(clause.OrConditions); ok {
			return false
		}
		if constrainsColumn(exprs[i], column) {
			return true
		}
	}
	return false
}

// isTenantEquality reports whether sql is exactly one equality against column,
// e.g. "tenant_id = ?", "`tenant_id` = ?" or "orders.tenant_id=?". Everything
// else — "<>", "!=", "LIKE", "IN", a disjunction, a subquery, a literal on the
// right-hand side — reports false so the plugin appends its own condition rather
// than trusting a comparison that may not restrict anything.
// isTenantEquality 报告 sql 是否恰好是一条针对 column 的等值比较，例如
// "tenant_id = ?"、"`tenant_id` = ?" 或 "orders.tenant_id=?"。其它情况——"<>"、
// "!="、"LIKE"、"IN"、析取、子查询、右侧为字面量——都返回 false，让插件追加自己的
// 条件，而不是相信一条可能什么都没限制的比较。
func isTenantEquality(sql, column string) bool {
	// One comparison only: ">=", "<=", "!=" and an ANDed pair all fail here.
	// 只允许一条比较：">="、"<="、"!=" 以及用 AND 连起来的另一条都在这里被拒。
	if strings.Count(sql, "=") != 1 {
		return false
	}
	left, right, _ := strings.Cut(sql, "=")
	// The value is always a placeholder: GORM substitutes it after the plugin
	// runs, so anything else is the caller's literal and is left to them.
	// 值一定是占位符：GORM 在插件之后才替换它，其它写法是调用方自己的字面量，不在
	// 这里判断。
	if strings.TrimSpace(right) != "?" {
		return false
	}
	left = strings.TrimSpace(left)
	if dot := strings.LastIndexByte(left, '.'); dot >= 0 {
		left = left[dot+1:]
	}
	return strings.EqualFold(strings.Trim(left, "`\""), column)
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

func isZeroValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.IsZero()
}
