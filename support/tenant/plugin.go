package tenant

import (
	"context"
	"database/sql/driver"
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

	// OverwriteExplicit decides whether an explicitly assigned tenant column on
	// INSERT is replaced by the context tenant. Nil (the zero value) means
	// overwrite: the plugin has no way to tell an administrator from a request
	// whose DTO happens to carry tenant_id, so trusting the caller's value by
	// default is a cross-tenant write waiting to happen. Set false only for a
	// deliberately privileged connection; cross-tenant writes must otherwise go
	// through tenant.Without, which skips the plugin entirely.
	// OverwriteExplicit 决定 INSERT 上显式赋值的租户列是否被上下文租户覆盖。nil（零值）
	// 表示覆盖：插件无从分辨"管理员"与"DTO 里碰巧有 tenant_id 的普通请求"，默认信任调用方
	// 的值等于留着一条跨租户写入的通路。只有刻意提权的连接才该设 false；其它跨租户写入
	// 一律走 tenant.Without——它会整条跳过插件。
	OverwriteExplicit *bool

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
		overwriteExplicit: opts.OverwritesExplicit(),
		onBypass:          opts.OnBypass,
		onUnresolved:      opts.OnUnresolved,
	}
}

// OverwritesExplicit reports whether an explicit INSERT tenant value is
// replaced. Nil means yes: the safe default. A false pointer is an explicit
// opt-out for a privileged connection, not a zero value that slipped through.
// OverwritesExplicit 报告 INSERT 上显式的租户值是否应被覆盖。nil 表示覆盖，即安全默认值；
// false 指针是特权连接的显式选择，不是漏设的零值。
func (o Options) OverwritesExplicit() bool {
	return o.OverwriteExplicit == nil || *o.OverwriteExplicit
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
	// An empty column would make LookUpField("") miss every model and turn the
	// plugin into a no-op. Reject it here, so both Instrument and a direct
	// db.Use(NewPlugin(...)) fail before any statement is rewritten.
	// 空列名会让 LookUpField("") 匹配不到任何模型，插件变成空操作。在这里拒绝，
	// Instrument 与直接 db.Use(NewPlugin(...)) 都会在改写语句之前失败。
	if p.column == "" {
		return ErrTenantColumnRequired
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
	if Bypassed(ctx) {
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
	id, ok := ResolveTenant(ctx, p.handler)
	if !ok {
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

// scope adds the tenant condition unless the caller already pinned the same
// tenant. There are three outcomes, and only the last one appends:
//
//   - no WHERE yet: the condition becomes the WHERE;
//   - a tenant equality bound to the effective tenant: nothing to add;
//   - anything else: AND the condition onto the caller's WHERE.
//
// The middle case is the only place this plugin trusts the caller, and it trusts
// a value, not a shape. `tenant_id = ?` carrying another tenant's ID selects
// that tenant's rows and reports nothing — the exact bypass the plugin exists to
// prevent — so a comparison that cannot prove equality appends instead.
// scope 追加租户条件，除非调用方已经把租户钉在同一个值上。只有三种结果，只有最后一种
// 会追加：
//
//   - 还没有 WHERE：条件直接成为 WHERE；
//   - 存在绑定值等于生效租户的租户等值条件：无需追加；
//   - 其它一切情况：把条件 AND 到调用方的 WHERE 上。
//
// 中间那种是插件唯一相信调用方的地方，而且信的是值、不是形状：`tenant_id = ?` 配上别的
// 租户 ID 会读到那个租户的数据且不报错——正是本插件存在的意义所要挡的那条绕过，所以证明
// 不了相等就必须追加。
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
	if exprsPinTenant(existing.Exprs, p.column, id) {
		return
	}
	where.Expression = clause.Where{Exprs: []clause.Expression{
		clause.AndConditions{Exprs: existing.Exprs},
		scope,
	}}
	stmt.Clauses["WHERE"] = where
}

// fill sets the tenant column on INSERT. Assignment is per row for every shape:
// GORM builds INSERT values by reading each row's field, so a batch-wide
// SetColumn cannot express "keep this row, fill that one" — and on a map slice
// it fails outright with "invalid field". Overwrite is the default, so a DTO
// carrying tenant_id from a request cannot write into that tenant; a privileged
// connection can opt out and keep explicit values.
//
// fill 给 INSERT 设置租户列。所有形态都按行赋值：GORM 构造 INSERT 值时逐行读字段，整批
// SetColumn 表达不了"这行保留、那行填充"——对 map 切片还会直接报 "invalid field"。默认覆盖，
// 请求结构体里带进来的 tenant_id 因此不能写入那个租户；特权连接可以显式关闭以保留原值。
func (p *Plugin) fill(db *gorm.DB) {
	if db == nil || db.Error != nil || db.Statement == nil {
		return
	}
	id, ok := p.resolve(db)
	if !ok || db.Error != nil {
		return
	}
	stmt := db.Statement

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
	setMap := func(row map[string]any) {
		if p.overwriteExplicit {
			row[p.column] = id
			return
		}
		if value, exists := row[p.column]; !exists || isZeroValue(value) {
			row[p.column] = id
		}
	}
	switch v := dest.(type) {
	case map[string]any:
		setMap(v)
		return
	case []map[string]any:
		for _, row := range v {
			setMap(row)
		}
		return
	}

	if stmt.Schema == nil {
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
		if p.overwriteExplicit || func() bool {
			_, zero := field.ValueOf(stmt.Context, rv)
			return zero
		}() {
			stmt.SetColumn(p.column, id, true)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			if p.overwriteExplicit {
				if err := field.Set(stmt.Context, rv.Index(i), id); err != nil {
					stmt.AddError(err)
				}
				continue
			}
			if _, zero := field.ValueOf(stmt.Context, rv.Index(i)); zero {
				if err := field.Set(stmt.Context, rv.Index(i), id); err != nil {
					stmt.AddError(err)
				}
			}
		}
	}
}

// pinsTenant walks the expression tree along the paths that restrict the result
// set: the top-level AND chain, AndConditions groups and nested Where.
// OrConditions and NotConditions are deliberately not descended into — a tenant
// column inside them does not scope the statement.
//
// An equality counts only when two things hold: it is ANDed with the rest of the
// condition, and its bound value is the effective tenant. The shape alone is
// what let `Where("tenant_id = ?", other)` read across tenants while looking
// filtered; a disjunction or a negation that happens to mention the column
// restricts nothing, which is what `tenant_id <> ?` and
// `tenant_id = ? OR is_public = 1` rely on. Raw SQL gets the same treatment: a
// lone `tenant_id = ?` comparison is recognised, its value comes from
// Expr.Vars, anything else is not recognised at all. A caller that really means
// another tenant uses tenant.WithTenant (or tenant.Without).
// pinsTenant 沿真正限制结果集的路径遍历表达式树：顶层 AND 链、AndConditions 分组与
// 嵌套 Where。刻意不进入 OrConditions 与 NotConditions——里面的租户列并不收窄语句范围。
//
// 等值条件要同时满足两点才算：与其余条件 AND 在一起，且绑定值就是生效租户。只认形状正是
// `Where("tenant_id = ?", 别的租户)` 看起来已过滤、实际跨租户读取的原因；字面上提到该列的
// 析取或取反并不限制结果集，`tenant_id <> ?`、`tenant_id = ? OR is_public = 1` 依赖的
// 就是这一点。手写 SQL 同样处理：只认单独一条 `tenant_id = ?`，值从 Expr.Vars 取，其它
// 一律不认。确实要操作别的租户时请用 tenant.WithTenant（或 tenant.Without）。
func pinsTenant(expr clause.Expression, column, id string) bool {
	switch v := expr.(type) {
	case nil:
		return false
	case clause.Eq:
		return columnName(v.Column) == column && sameTenant(v.Value, id)
	case clause.AndConditions:
		for _, inner := range v.Exprs {
			if pinsTenant(inner, column, id) {
				return true
			}
		}
	case clause.Where:
		return exprsPinTenant(v.Exprs, column, id)
	case clause.Expr:
		return isTenantEquality(v.SQL, column) &&
			len(v.Vars) == 1 && sameTenant(v.Vars[0], id)
	}
	return false
}

// exprsPinTenant walks one conjunction list from the end.
//
// GORM keeps the expressions in source order and renders an OrConditions as
// "OR ...", so whatever precedes the last OrConditions is the left operand of a
// disjunction — `Where("tenant_id = ?").Or("code = ?")` builds exactly that, and
// the tenant equality there does not scope the query even when its value is the
// right one. Only expressions after it are ANDed with the whole condition, so
// the scan stops at the first OrConditions from the right.
// exprsPinTenant 从末尾遍历一组合取表达式。
//
// GORM 按书写顺序保存表达式，并把 OrConditions 渲染成 "OR ..."，因此最后一个
// OrConditions 之前的内容都是析取的左操作数——`Where("tenant_id = ?").Or("code = ?")`
// 构造的正是这种形态，其中的租户等值即使值是对的也不收窄查询。只有它之后（索引更大）的
// 表达式才是与整体 AND 在一起的，所以扫描在从右往左遇到的第一个 OrConditions 处停止。
func exprsPinTenant(exprs []clause.Expression, column, id string) bool {
	for i := len(exprs) - 1; i >= 0; i-- {
		if _, ok := exprs[i].(clause.OrConditions); ok {
			return false
		}
		if pinsTenant(exprs[i], column, id) {
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
	// runs, which leaves the value in Expr.Vars for pinsTenant to compare. A
	// literal on the right is the caller's own and stays unrecognised.
	// 值一定是占位符：GORM 在插件之后才替换它，替换前它就留在 Expr.Vars 里，供
	// pinsTenant 比较。右侧写死字面量是调用方自己的值，不在这里判断。
	if strings.TrimSpace(right) != "?" {
		return false
	}
	left = strings.TrimSpace(left)
	if dot := strings.LastIndexByte(left, '.'); dot >= 0 {
		left = left[dot+1:]
	}
	return strings.EqualFold(strings.Trim(left, "`\""), column)
}

// sameTenant reports whether a bound value is the given tenant.
//
// Comparison is by string form because the column's Go type belongs to the
// application: the same tenant may arrive as "42", 42, int64(42) or a named
// string type. Values implementing driver.Valuer — sql.NullString, uuid.UUID —
// are asked for their database form first.
//
// A false result is always safe: the caller's condition stays and the plugin
// appends its own, so an unrecognised value costs a redundant predicate instead
// of a cross-tenant read. A wrong "true" is what would be dangerous, and it
// requires the two string forms to be identical.
// sameTenant 报告绑定值是否就是给定租户。
//
// 按字符串形态比较，因为租户列的 Go 类型由应用决定：同一个租户可能是 "42"、42、int64(42)
// 或一个命名 string 类型。实现了 driver.Valuer 的值（sql.NullString、uuid.UUID）先问它
// 的数据库形态。
//
// 返回 false 永远是安全的：调用方的条件保留，插件追加自己的条件，认不出的值只多一个谓词，
// 不会变成跨租户读取。危险的是错误的 true，而它要求两边字符串完全一致。
func sameTenant(v any, id string) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x == id
	case []byte:
		return string(x) == id
	case driver.Valuer:
		got, err := x.Value()
		if err != nil {
			return false
		}
		return sameTenant(got, id)
	}
	return fmt.Sprint(v) == id
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
