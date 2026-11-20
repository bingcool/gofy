// Package sqlbuilder builds SQL statements; it never executes them.
// 包 sqlbuilder 只构造 SQL 语句，从不执行。
//
// It re-exports the builder of entgo.io/ent/dialect/sql, so business code imports
// one support package instead of an ORM, and adds Build, which turns the error a
// builder keeps to itself into a returned error. What comes with it is only the
// string constructor: Select, Table, With, Union, Predicate, locking. What does
// not come with it is any part of ent's runtime — no ORM, no schema DSL, no
// codegen, no *ent.Client, no entsql.Open. Data access stays on gorm, per the
// 2026-11-15 "no ent client" decision; this package does not reopen it.
// 它把 entgo.io/ent/dialect/sql 的构造器引到一处，业务代码只 import 一个 support 包，
// 并补上 Build：builder 内部存着的错误会变成返回错误。随之而来的只有字符串构造能力
// （Select / Table / With / Union / Predicate / 行锁），不含 ent 运行时的任何部分——
// 没有 ORM、没有 schema DSL、没有 codegen、没有 *ent.Client、没有 entsql.Open。
// 数据访问仍走 gorm（2026-11-15「不引入 ent client」的决策不变），本包不重开这个问题。
//
// Execution goes back through gorm, which keeps every existing mechanism working:
// 执行仍然交回 gorm，现有机制一个不动：
//
//	db, err := appio.WithContext(ctx).DBConnect(ctx, appdb.DefaultInstance)
//	stmt, args, err := sqlbuilder.Build(sel)
//	err = db.Raw(stmt, args...).Scan(&rows).Error
//
// The handle from DBConnect carries the transaction found in ctx and is already
// bound to it, so the statement joins an ambient transaction and is logged,
// masked, traced and timed like any other. Do not take *sql.DB from gorm and
// execute there: db.DB() returns the pool even inside a transaction (it digs the
// field out of the *sql.Tx), which loses the transaction, the SQL log and the
// generation pinning at once.
// 从 DBConnect 拿到的句柄已经带上 ctx 中的事务、也绑定了 ctx，因此语句会加入当前事务，
// 并且照常被记录、脱敏、打 trace、统计慢 SQL。不要从 gorm 取 *sql.DB 去执行：
// db.DB() 即使在事务里返回的也是连接池（它把 *sql.Tx 里的字段掏了出来），会同时丢掉
// 事务、SQL 日志与代际固定。
//
// # Constraints 使用约束
//
// A Predicate is stateful: Query resets it and replays its callbacks. Calling
// Query twice concurrently on the same *Predicate is a data race, and caching one
// across requests has the same effect. Build predicates inside the function that
// consumes them; never store them in a struct field or share them between
// goroutines.
// Predicate 是有状态的：Query 会先 Reset 自己再重放回调。同一个 *Predicate 被两个
// goroutine 同时 Query 就是 data race，跨请求缓存同样如此。谓词在函数内构造、函数内
// 消费，不存字段、不跨 goroutine。
//
// Never pass a slice to EQ: the builder treats it as one argument while gorm
// expands it as an IN list, and the two interpretations diverge. Build the list
// with In(col, vals...).
// 不要把 slice 传给 EQ：builder 把它当一个参数，gorm 又把它当 IN 列表展开，两种解释
// 会打架。列表条件用 In(col, vals...)。
//
// Table names are literals here; they do not pass through gorm's NamingStrategy.
// A project with a table prefix or pluralization must take the name from
// model.TableName() instead of typing it.
// 这里的表名是字面量，不经过 gorm 的 NamingStrategy。配了表前缀或复数化的项目必须从
// model.TableName() 取，不要手写。
//
// The dialect is a decision the caller makes. The default quotes with backticks,
// which MySQL and SQLite both accept; PostgreSQL needs Dialect(DialectPostgres)
// to switch to double quotes. Never pass dialect.SQLite: it renders
// `"orders"."id"` — backticks around a double-quoted name — and SQLite then looks
// for a column literally called `"orders"."id"`, failing with "no such column"
// while the column is right there. ForUpdate under that dialect also records an
// error, so Build fails. SQLite only appears in tests; use the default dialect.
// 方言要由调用方决定。默认用反引号，MySQL 与 SQLite 都接受；PostgreSQL 需要显式切换成
// 双引号。绝不要传 dialect.SQLite：它会渲染出 `"orders"."id"`（反引号套双引号），
// SQLite 于是去找一个名为 `"orders"."id"` 的列，报 "no such column"，而列明明存在；
// 该方言下 ForUpdate 还会记录错误，让 Build 失败。SQLite 只出现在测试里，用默认方言。
//
// Query() returns no error. Construction errors are collected in the builder, so
// always go through Build; calling sel.Query() directly drops them silently.
// Query() 不返回错误，构造期错误被收集在 builder 里，所以一律走 Build；直接调用
// sel.Query() 会静默吞掉错误。
//
// # Multi-tenancy 多租户
//
// support/tenant hooks Query/Update/Delete/Create and deliberately not Raw, so a
// statement passed to db.Raw gets no automatic tenant_id condition. A predicate
// fragment passed to db.Where becomes a clause.Expr, which the plugin does
// inspect (equality only). So: express filters as predicates and hand them to
// Where whenever possible; when a whole statement must go to Raw and the table
// has a tenant column, include tenant_id yourself — TenantScope returns the
// predicate, or false when the request carries no tenant.
// support/tenant 只挂 Query/Update/Delete/Create，刻意不挂 Raw，因此交给 db.Raw 的语句
// 不会自动补 tenant_id。交给 db.Where 的谓词片段会变成 clause.Expr，插件会检查它（仅
// 等值）。因此：能用谓词表达的条件就交给 Where；整条语句必须走 Raw 且表含租户列时，
// 自己带上 tenant_id——TenantScope 返回该谓词，ctx 中没有租户时返回 false。
//
// The exit cost of this package is deleting one directory: nothing else depends on
// it, and no state is created at import time.
// 本包的退出成本是删掉一个目录：没有别的东西依赖它，导入时也不创建任何状态。
package sqlbuilder

import (
	"context"
	"fmt"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/bingcool/gofy/support/frameworkcontext"
)

// Aliases of the upstream types, so a call site never names ent.
// 上游类型的别名，调用处因此不需要出现 ent 的名字。
type (
	// Selector builds a SELECT statement.
	// Selector 构造 SELECT 语句。
	Selector = entsql.Selector
	// SelectTable is a table reference usable as a column prefix.
	// SelectTable 是可作为列前缀的表引用。
	SelectTable = entsql.SelectTable
	// Predicate is a condition tree; see the statefulness note above.
	// Predicate 是条件树；注意上面的有状态说明。
	Predicate = entsql.Predicate
	// Builder accumulates a statement and its arguments.
	// Builder 累积语句与参数。
	Builder = entsql.Builder
	// Querier is anything that can render itself to SQL and args.
	// Querier 是任何能渲染成 SQL 与参数的对象。
	Querier = entsql.Querier
	// LockOption configures a row lock clause.
	// LockOption 配置行锁子句。
	LockOption = entsql.LockOption
	// LockAction is the action of a row lock, such as SkipLocked.
	// LockAction 是行锁的动作，例如 SkipLocked。
	LockAction = entsql.LockAction
	// WithBuilder builds a CTE; it is what With and WithRecursive return.
	// WithBuilder 构造 CTE；With 与 WithRecursive 返回它。
	WithBuilder = entsql.WithBuilder
	// DialectBuilder starts a statement that quotes for one dialect.
	// DialectBuilder 开始一条按指定方言加引号的语句。
	DialectBuilder = entsql.DialectBuilder
)

// Statement constructors.
//
// These are wrappers rather than aliases. `var Table = entsql.Table` compiles
// just as well, but a package-level function value is assignable from outside the
// package (`sqlbuilder.Table = somethingElse`) and both godoc and the IDE symbol
// list file it under variables, so it is never found where a function is
// expected. A wrapper costs one line and keeps the API read-only and listed under
// functions; it also fails to compile if upstream changes a signature, which is
// exactly when this package must be looked at.
// 语句构造器。
//
// 这些是包装函数而不是别名。写成 `var Table = entsql.Table` 同样能编译，但包级函数值
// 从包外可赋值（`sqlbuilder.Table = 别的函数`），而且 godoc 与 IDE 的符号列表把它归在
// 变量下——按「找函数」的思路永远看不到它。包装只多一行，换来只读的 API、在函数分组里
// 可见，以及上游改签名时编译失败（那正是必须有人来看这个包的时候）。
func Select(columns ...string) *Selector { return entsql.Select(columns...) }

// SelectExpr selects arbitrary queriers, for expressions without a dedicated
// builder (window functions, for example).
// SelectExpr 选择任意 Querier，用于没有专用构造器的表达式（例如窗口函数）。
func SelectExpr(exprs ...Querier) *Selector { return entsql.SelectExpr(exprs...) }

// Table references a table by name. The name is a literal and does not go
// through gorm's NamingStrategy.
// Table 按名字引用一张表。名字是字面量，不经过 gorm 的 NamingStrategy。
func Table(name string) *SelectTable { return entsql.Table(name) }

// Expr wraps a raw SQL fragment with its own arguments.
// Expr 把一段裸 SQL 片段和它自己的参数包起来。
func Expr(expr string, args ...any) Querier { return entsql.Expr(expr, args...) }

// Count renders COUNT(ident) and returns the string, so it can be passed to
// Select, SelectExpr or a comparison.
// Count 渲染 COUNT(ident) 并返回字符串，因此可以直接交给 Select、SelectExpr 或比较。
func Count(ident string) string { return entsql.Count(ident) }

// With starts a common table expression; it must be attached to the statement
// with Prefix. WithRecursive additionally allows the CTE to reference itself.
// With 开始一个 CTE，必须经 Prefix 挂到语句上。WithRecursive 额外允许自引用。
func With(name string, columns ...string) *WithBuilder {
	return entsql.With(name, columns...)
}

// WithRecursive is With plus the RECURSIVE keyword.
// WithRecursive 是 With 加 RECURSIVE 关键字。
func WithRecursive(name string, columns ...string) *WithBuilder {
	return entsql.WithRecursive(name, columns...)
}

// Dialect starts a statement whose identifiers are quoted for that dialect. See
// the package doc: the default is correct for MySQL and SQLite, and PostgreSQL
// is the one case that needs this.
// Dialect 开始一条按该方言给标识符加引号的语句。见包注释：默认值对 MySQL 与 SQLite
// 都正确，真正需要它的是 PostgreSQL。
func Dialect(name string) *DialectBuilder { return entsql.Dialect(name) }

// Condition combinators and comparisons. Each returns a Predicate tree that can
// be nested freely; Build renders it, or gorm renders it when passed to Where.
// 条件组合与比较。每个都返回可自由嵌套的谓词树：由 Build 渲染，或交给 gorm 的 Where
// 由 gorm 渲染。
func And(preds ...*Predicate) *Predicate { return entsql.And(preds...) }

// Or groups predicates as alternatives; the parentheses are emitted, so it can
// sit inside And without changing meaning.
// Or 把谓词组合成「或」；括号会一起输出，因此嵌在 And 里不会改变语义。
func Or(preds ...*Predicate) *Predicate { return entsql.Or(preds...) }

// Not negates one predicate or a group.
// Not 对一个谓词或一组谓词取反。
func Not(pred *Predicate) *Predicate { return entsql.Not(pred) }

// EQ, NEQ, GT, GTE, LT and LTE compare a column with one value. The column is
// quoted as written, so it may carry a table prefix ("orders.id").
// EQ、NEQ、GT、GTE、LT、LTE 把列与一个值比较。列按原样加引号，因此可以带表前缀
// （"orders.id"）。
func EQ(col string, value any) *Predicate { return entsql.EQ(col, value) }

// NEQ is the inequality comparison.
// NEQ 是不等比较。
func NEQ(col string, value any) *Predicate { return entsql.NEQ(col, value) }

// GT is the greater-than comparison.
// GT 是大于比较。
func GT(col string, value any) *Predicate { return entsql.GT(col, value) }

// GTE is the greater-than-or-equal comparison.
// GTE 是大于等于比较。
func GTE(col string, value any) *Predicate { return entsql.GTE(col, value) }

// LT is the less-than comparison.
// LT 是小于比较。
func LT(col string, value any) *Predicate { return entsql.LT(col, value) }

// LTE is the less-than-or-equal comparison.
// LTE 是小于等于比较。
func LTE(col string, value any) *Predicate { return entsql.LTE(col, value) }

// In matches a column against a list; it is the only way to build IN — passing a
// slice to EQ would be read as one argument here and as a list by gorm.
// In 用一个列表匹配列；构造 IN 只能用它——把 slice 传给 EQ，这里会当成一个参数，
// gorm 那边又会当成列表。
func In(col string, args ...any) *Predicate { return entsql.In(col, args...) }

// NotIn is the negated In.
// NotIn 是 In 的否定。
func NotIn(col string, args ...any) *Predicate { return entsql.NotIn(col, args...) }

// IsNull and NotNull test for NULL.
// IsNull 与 NotNull 判断 NULL。
func IsNull(col string) *Predicate { return entsql.IsNull(col) }

// NotNull tests for a non-NULL value.
// NotNull 判断非 NULL。
func NotNull(col string) *Predicate { return entsql.NotNull(col) }

// Like matches a column against a pattern; the caller supplies the % signs.
// Like 用模式匹配列；百分号由调用方自己给出。
func Like(col, pattern string) *Predicate { return entsql.Like(col, pattern) }

// Exists and NotExists take a subquery. Select columns with SelectExpr(Expr("1"))
// rather than Select("1"), which would quote the constant as an identifier.
// Exists 与 NotExists 接收子查询。列要用 SelectExpr(Expr("1")) 选，不能用
// Select("1")——后者会把常量当成标识符加引号。
func Exists(query Querier) *Predicate { return entsql.Exists(query) }

// NotExists is the negated Exists.
// NotExists 是 Exists 的否定。
func NotExists(query Querier) *Predicate { return entsql.NotExists(query) }

// ColumnsEQ and ColumnsNEQ compare two columns instead of a column and a value;
// they are what correlated subqueries need.
// ColumnsEQ 与 ColumnsNEQ 比较两个列而不是列与值；关联子查询需要它们。
func ColumnsEQ(col1, col2 string) *Predicate { return entsql.ColumnsEQ(col1, col2) }

// ColumnsNEQ is the negated ColumnsEQ.
// ColumnsNEQ 是 ColumnsEQ 的否定。
func ColumnsNEQ(col1, col2 string) *Predicate { return entsql.ColumnsNEQ(col1, col2) }

// Row lock options. A lock only holds until the surrounding transaction ends, so
// a locking statement must run on a handle that is inside one.
// 行锁选项。锁只持有到事务结束，因此加锁语句必须跑在事务内的句柄上。
func WithLockAction(action LockAction) LockOption { return entsql.WithLockAction(action) }

// WithLockTables scopes the lock to the named tables.
// WithLockTables 把锁限定到指定表。
func WithLockTables(tables ...string) LockOption { return entsql.WithLockTables(tables...) }

// Lock actions accepted by WithLockAction.
// WithLockAction 接受的动作。
const (
	SkipLocked = entsql.SkipLocked
	NoWait     = entsql.NoWait
)

// Dialect names Dialect accepts, so a project targeting PostgreSQL does not
// import ent for one string constant. MySQL needs no call at all — the default
// already quotes with backticks. SQLite is deliberately absent: it must not be
// passed, see the package doc.
// Dialect 接受的方言名，需要 PostgreSQL 的项目因此不必为了一个字符串常量去 import
// ent。MySQL 完全不需要调用——默认就用反引号。SQLite 刻意没有：它不能传，见包注释。
const (
	DialectMySQL    = dialect.MySQL
	DialectPostgres = dialect.Postgres
)

// DefaultTenantColumn is the column TenantScope filters on. It mirrors
// tenant.DefaultColumn; the two are pinned by a test instead of one importing the
// other, because support/tenant pulls in gorm and this package must not.
// DefaultTenantColumn 是 TenantScope 使用的租户列。它与 tenant.DefaultColumn 一致，
// 用测试互相钉住而不是互相 import：support/tenant 会带进 gorm，而本包不能依赖它。
const DefaultTenantColumn = "tenant_id"

// Build renders q to SQL and its arguments, and returns the error the builder
// would otherwise keep to itself.
//
// On error the statement and args are empty: a partially rendered statement is
// never safe to execute, and returning it invites a caller that ignores the error
// to run something subtly different from what was built.
// Build 把 q 渲染成 SQL 与参数，并把 builder 本来会自己留着的错误带出来。
//
// 出错时语句与参数都为空：渲染了一半的 SQL 不能执行，把它返回出去只会让忽略错误的调用
// 方跑一条与预期微妙不同的语句。
func Build(q Querier) (string, []any, error) {
	stmt, args := q.Query()
	if e, ok := q.(interface{ Err() error }); ok {
		if err := e.Err(); err != nil {
			return "", nil, fmt.Errorf("sqlbuilder: %w", err)
		}
	}
	return stmt, args, nil
}

// TenantScope returns the tenant equality predicate of the current request, and
// false when ctx carries no tenant. The caller decides whether a missing tenant
// is an error or a reason to skip the filter, so this function does not guess.
//
// The column is DefaultTenantColumn and the value comes from ctx, matching what
// support/tenant injects; a statement built with this predicate still needs to be
// executed through db.Where or db.Raw as described in the package doc.
// TenantScope 返回当前请求的租户等值谓词，ctx 中没有租户时返回 false。缺租户是报错还是
// 跳过过滤由调用方决定，本函数不替它猜。
//
// 列名固定为 DefaultTenantColumn，取值来自 ctx，与 support/tenant 注入的一致；带上它的
// 语句仍需按包注释所述经 db.Where 或 db.Raw 执行。
func TenantScope(ctx context.Context, table *SelectTable) (*Predicate, bool) {
	tid := frameworkcontext.TenantID(ctx)
	if tid == "" {
		return nil, false
	}
	return EQ(table.C(DefaultTenantColumn), tid), true
}
