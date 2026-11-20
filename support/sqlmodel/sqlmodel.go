// Package sqlmodel builds builder statements for a GORM model: the table name
// comes from the model, and the tenant condition is applied when the model
// declares the tenant column.
//
// support/sqlbuilder constructs SQL and knows nothing about models — it must also
// stay free of gorm, which a guard in shell/check-imports.sh enforces. This
// package is the other half: it may depend on gorm, reads the name from
// Model.TableName(), and decides tenant-awareness through schema.LookUpField —
// the same call support/tenant makes before it rewrites a statement — so the
// builder path and the gorm path cannot disagree about whether a table is
// tenant-scoped.
//
// The decision is fail-closed. A model that declares the tenant column, with no
// tenant in ctx, is an error, never an unfiltered query: a script or worker that
// forgot to carry a tenant would otherwise read every tenant's rows. Skipping the
// filter is possible on purpose, through tenant.Without, exactly as with the
// plugin, or for a whole deployment through Options.Disabled — the counterpart of
// tenant.enabled, so both isolation paths are switched together.
// 包 sqlmodel 为 GORM 模型构造 builder 语句：表名取自模型，模型声明了租户列时自动施加
// 租户条件。
//
// support/sqlbuilder 只拼 SQL、不认识模型，而且必须与 gorm 无关（shell/check-imports.sh
// 有守卫）。本包是另一半：它可以依赖 gorm，表名取自 Model.TableName()，租户列由
// schema.LookUpField 判定——与 support/tenant 改写语句前用的是同一个判断——因此构造器
// 路径与 gorm 路径不可能对「这张表是否带租户」给出不同答案。
//
// 判断是失败关闭的。模型声明了租户列而 ctx 里没有租户时返回错误，绝不退化成不带过滤的
// 查询：脚本或 worker 忘了带租户，否则就会读到所有租户的数据。要跳过过滤只能显式用
// tenant.Without，与插件一致；整条路径关闭用 Options.Disabled——它是 tenant.enabled 的
// 对应项，两条隔离路径因此一起开关。
//
//	sel, err := sqlmodel.Select(ctx, ordermodel.Order{}, "id", "user_id")
//	if err != nil {
//	    return nil, err // 表带租户列而 ctx 无租户时命中 tenant.ErrTenantRequired
//	}
//	orders := sel.Table() // *sqlbuilder.SelectTable，用于 C()/Join
//	sel = sel.Where(sqlbuilder.EQ(orders.C("status"), "paid"))
//	stmt, args, err := sqlbuilder.Build(sel)
package sqlmodel

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"gorm.io/gorm/schema"

	"github.com/bingcool/gofy/support/sqlbuilder"
	"github.com/bingcool/gofy/support/tenant"
)

// Model is what a statement needs from a table: its name. GORM's schema.Tabler
// declares the same method, so an existing model satisfies this interface
// without extra code.
// Model 是语句需要从表上知道的全部：表名。GORM 的 schema.Tabler 声明了同一个方法，
// 因此已有的模型不需要额外代码就能满足这个接口。
type Model interface {
	TableName() string
}

// ErrModelRequired is returned for a nil model or one whose TableName is empty.
// ErrModelRequired 在 model 为 nil 或 TableName 为空时返回。
var ErrModelRequired = errors.New("sqlmodel: model is required")

// Options configures a Resolver. The zero value is usable and matches the
// defaults of the tenant plugin: ContextHandler, tenant_id, the naming strategy
// GORM applies out of the box, and the builder's default dialect.
// Options 配置 Resolver。零值即可用，且与租户插件的默认值一致：ContextHandler、
// tenant_id、GORM 默认的命名策略，以及构造器的默认方言。
type Options struct {
	// Handler supplies the tenant ID and column, exactly as it does for the
	// plugin. Nil uses tenant.ContextHandler. Pass the same Handler the
	// connection was built with, or the two paths will look for different
	// columns.
	// Handler 提供租户 ID 与列名，与插件用法相同。为 nil 时使用 tenant.ContextHandler。
	// 请传入连接所用的同一个 Handler，否则两条路径会找不同的列。
	Handler tenant.Handler

	// Namer must be the NamingStrategy the *gorm.DB was built with. Nil uses
	// schema.NamingStrategy{}. It decides which column TenantID maps to, so a
	// project that renames columns has to pass its own namer here as well.
	// Namer 必须是构建 *gorm.DB 时所用的 NamingStrategy，为 nil 时用
	// schema.NamingStrategy{}。它决定 TenantID 映射到哪一列，因此改过列名的项目必须在这里
	// 也传入同一个 namer。
	Namer schema.Namer

	// Dialect is passed to the builder. Empty uses its default (backticks, valid
	// for MySQL and SQLite); PostgreSQL needs sqlbuilder.DialectPostgres. Never
	// pass a SQLite dialect name — see support/sqlbuilder's README.
	// Dialect 传给构造器。为空时用它自己的默认值（反引号，MySQL 与 SQLite 都接受）；
	// PostgreSQL 需要 sqlbuilder.DialectPostgres。绝不要传 SQLite 的方言名——见
	// support/sqlbuilder 的 README。
	Dialect string

	// Disabled turns the tenant condition off, mirroring tenant.enabled in the
	// connection config. The zero value keeps isolation on, matching the
	// deployment default: forgetting to set it must not remove the filter.
	// Tenant-scoped models then behave like plain tables when it is true: no
	// condition, and no error when the context carries no tenant. Read once, at
	// New — the same way the plugin is installed once — so a Resolver cannot be
	// switched per request.
	//
	// It exists because the two isolation paths must be switched together: with
	// the plugin off and this on, a repository statement reads every tenant while
	// a builder statement is still filtered, and neither behaviour is visible in
	// the config.
	// Disabled 关闭租户条件，对应连接配置里的 tenant.enabled。零值保持开启，与部署默认值
	// 一致：漏设不能等于去掉过滤。为 true 时带租户列的模型按普通表处理：不加条件，ctx 无
	// 租户也不报错。它在 New 时读一次——与插件安装一次同理——因此一个 Resolver 不能按请求
	// 切换。
	//
	// 它存在的原因是两条隔离路径必须一起开关：插件关着而这里开着时，仓储语句读到所有租户，
	// 构造器语句却仍被过滤，而配置里看不出这件事。
	Disabled bool
}

// Resolver turns models into builder tables. It caches parsed schemas, so create
// one per connection configuration and reuse it; it is safe for concurrent use.
// Resolver 把模型变成构造器里的表。它缓存解析后的 schema，因此每个连接配置创建一个并
// 复用；它是并发安全的。
type Resolver struct {
	column   string
	handler  tenant.Handler
	namer    schema.Namer
	dialect  string
	disabled bool
	schemas  sync.Map
}

// New builds a Resolver. A nil Handler and Namer fall back to the plugin
// defaults; the column comes from the Handler, so the two paths read the same
// configuration.
// New 构造 Resolver。Handler 与 Namer 为 nil 时回退到插件默认值；列名取自 Handler，
// 因此两条路径读的是同一份配置。
func New(opts Options) *Resolver {
	r := &Resolver{
		handler:  opts.Handler,
		namer:    opts.Namer,
		dialect:  opts.Dialect,
		disabled: opts.Disabled,
	}
	if r.handler == nil {
		r.handler = tenant.ContextHandler{}
	}
	if r.namer == nil {
		r.namer = schema.NamingStrategy{}
	}
	r.column = r.handler.Column()
	return r
}

// Default is the Resolver behind the package-level Select: ContextHandler,
// tenant_id, GORM's default naming and the builder's default dialect. A project
// that configured either of them builds its own Resolver with New.
// Default 是包级 Select 使用的 Resolver：ContextHandler、tenant_id、GORM 默认命名与
// 构造器默认方言。配置过其中任一项的项目应当用 New 构造自己的 Resolver。
var Default = New(Options{})

// Select builds `SELECT columns FROM <model table>`, with the tenant condition
// already attached when the model declares the tenant column. Columns are plain
// names qualified with the table; pass none to select `*`. A caller that needs an
// expression adds it afterwards, and gets the table back from Selector.Table():
//
//	sel, err := sqlmodel.Select(ctx, ordermodel.Order{}, "id", "amount")
//	if err != nil {
//	    return nil, err
//	}
//	sel = sel.AppendSelectExprAs(sqlbuilder.Sum(sel.Table().C("amount")), "total")
//
// Select 构造 `SELECT columns FROM <模型表>`，模型声明了租户列时租户条件已经挂好。列是
// 裸列名，由本函数加上表限定；不传列表示 `*`。需要表达式时随后追加，并通过
// Selector.Table() 取回表对象来限定列。
func Select(ctx context.Context, m Model, columns ...string) (*sqlbuilder.Selector, error) {
	return Default.Select(ctx, m, columns...)
}

// Select is the method form of the package-level Select.
// Select 是包级 Select 的方法形式。
func (r *Resolver) Select(ctx context.Context, m Model, columns ...string) (*sqlbuilder.Selector, error) {
	ref, tenantPred, err := r.Table(ctx, m)
	if err != nil {
		return nil, err
	}

	// From stamps the dialect into ref, so the columns and the predicate are
	// quoted the way this statement needs. AppendSelect is used because Select's
	// arguments would be evaluated before that happens.
	sel := sqlbuilder.Dialect(r.dialect).Select().From(ref)
	if len(columns) == 0 {
		columns = []string{"*"}
	}
	sel = sel.AppendSelect(ref.Columns(columns...)...)
	if tenantPred != nil {
		sel = sel.Where(tenantPred)
	}
	return sel, nil
}

// Table returns the builder reference for a model and, when the table carries
// the tenant column, the predicate for this request. The predicate is nil for a
// table without the column, for a bypassed context, and when the resolver was
// built with Disabled.
//
// Select has already attached it. Use Table directly only for a statement Select
// does not cover (a JOIN, a CTE, a hand-built UPDATE) — and then attach the
// predicate to that statement's Where yourself, per table: in a join every
// tenant-scoped table needs its own condition.
// Table 返回模型的构造器表引用，以及表带租户列时本请求的租户谓词。表不含该列、ctx 被
// 显式绕过、或 Resolver 以 Disabled 构造时，谓词为 nil。
//
// Select 已经把它挂好了。只有在 Select 覆盖不了的语句里（JOIN、CTE、手写 UPDATE）才直接
// 用 Table——此时谓词要自己挂到那条语句的 Where 上，且按表挂：一条 join 里每张带租户列的
// 表都需要自己的条件。
func (r *Resolver) Table(ctx context.Context, m Model) (*sqlbuilder.SelectTable, *sqlbuilder.Predicate, error) {
	name, err := modelName(m)
	if err != nil {
		return nil, nil, err
	}

	ref := r.stamp(sqlbuilder.Table(name))
	// Disabled is checked before anything else: isolation is off for this
	// deployment, so there is nothing to decide and no error to raise.
	if r.disabled {
		return ref, nil, nil
	}
	// Bypass comes next, like the plugin: Without must win on every table,
	// including one whose schema cannot be parsed.
	if tenant.Bypassed(ctx) {
		return ref, nil, nil
	}

	parsed, err := r.parse(m)
	if err != nil {
		return nil, nil, fmt.Errorf("sqlmodel: parse %s: %w", name, err)
	}
	// LookUpField matches the field name and the column name, the same call
	// support/tenant uses to decide whether a statement is tenant-aware.
	if parsed.LookUpField(r.column) == nil {
		return ref, nil, nil
	}

	id := r.tenantID(ctx)
	if id == "" {
		return nil, nil, fmt.Errorf("%w: table %s", tenant.ErrTenantRequired, name)
	}
	return ref, sqlbuilder.EQ(ref.C(r.column), id), nil
}

// stamp writes the resolver's dialect into the table. C quotes with the table's
// own dialect, and From only hands it over when a statement is built — which is
// after the column list and the predicate have been rendered to strings.
// stamp 把 Resolver 的方言写进表对象。C 用表自己的方言加引号，而 From 要等到构造语句时
// 才把方言递过去——那时列名与谓词早就变成字符串了。
func (r *Resolver) stamp(ref *sqlbuilder.SelectTable) *sqlbuilder.SelectTable {
	if r.dialect != "" {
		// From is the only exported way to give a table its dialect.
		sqlbuilder.Dialect(r.dialect).Select().From(ref)
	}
	return ref
}

// parse reads the model's schema. The cache lives on the Resolver because the
// parsed column names depend on its Namer.
// parse 读取模型的 schema。缓存挂在 Resolver 上，因为解析出的列名取决于它的 Namer。
func (r *Resolver) parse(m Model) (*schema.Schema, error) {
	return schema.Parse(m, &r.schemas, r.namer)
}

// tenantID mirrors the plugin: an explicit WithTenant overrides the Handler, and
// an empty result means "no tenant", which the caller turns into an error.
// tenantID 与插件一致：WithTenant 的显式值压过 Handler，空结果表示「无租户」，由调用方
// 转成错误。
func (r *Resolver) tenantID(ctx context.Context) string {
	if ctx == nil {
		ctx = context.Background()
	}
	if id, ok := tenant.OverrideTenant(ctx); ok {
		return strings.TrimSpace(id)
	}
	return strings.TrimSpace(r.handler.TenantID(ctx))
}

// modelName validates the interface value. A typed nil pointer satisfies the
// interface but panics the moment TableName is called.
// modelName 校验接口值。带类型的 nil 指针也满足接口，但一调用 TableName 就会 panic。
func modelName(m Model) (string, error) {
	if m == nil {
		return "", ErrModelRequired
	}
	if v := reflect.ValueOf(m); v.Kind() == reflect.Ptr && v.IsNil() {
		return "", ErrModelRequired
	}
	name := strings.TrimSpace(m.TableName())
	if name == "" {
		return "", fmt.Errorf("%w: empty TableName()", ErrModelRequired)
	}
	return name, nil
}
