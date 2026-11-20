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
// The dialect also changes the placeholders: under DialectPostgres the builder
// writes $1, $2 instead of ?. A fragment built with Expr, ExprP or a literal "?"
// inside Raw keeps its question marks, so mixing them into a PostgreSQL-dialect
// statement produces both kinds at once. Do not mix; a statement is either shaped
// for PostgreSQL throughout or not at all.
// 方言还会改变占位符：DialectPostgres 下构造器写 $1、$2，而不是 ?。用 Expr、ExprP 或
// Raw 里写死 "?" 的片段仍保留问号，混进 PostgreSQL 方言的语句就会同时出现两种占位符。
// 不要混用：一条语句要么整体按 PostgreSQL 的形态构造，要么整体不这么做。
//
// Conflict resolution needs the dialect: write Dialect(DialectMySQL).Insert(...).
// Insert(table) on its own builds a plain INSERT, and adding OnConflict to it
// emits no ON DUPLICATE KEY UPDATE / ON CONFLICT clause at all — upstream switches
// on the dialect and has no default branch — so the statement is invalid without
// any error being recorded.
// 冲突处理需要方言：写成 Dialect(DialectMySQL).Insert(...)。单独用 Insert(table) 造的是
// 普通 INSERT，再挂 OnConflict 也不会输出 ON DUPLICATE KEY UPDATE / ON CONFLICT 子句——
// 上游按方言分支且没有 default 分支——语句因此无效，而且不会记录任何错误。
//
// A *SelectTable is stateful too: From and Join copy the statement's dialect into
// the table, so one table value must not be reused across statements of different
// dialects. Reusing it produces nested quoting (`"orders"."id"` wrapped in
// backticks). Build tables next to the statement that uses them.
// *SelectTable 同样是有状态的：From 与 Join 会把语句的方言写回表对象，因此同一个表值不能
// 跨不同方言的语句复用，复用会产出嵌套引号（`"orders"."id"` 外面再套反引号）。表对象应当
// 与使用它的语句一起构造。
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
	// TableView is what Union accepts: a table, a selector or a CTE.
	// TableView 是 Union 接受的实参：表、SELECT 或 CTE。
	TableView = entsql.TableView
	// LockStrength is the strength Selector.For takes ("UPDATE", "SHARE").
	// LockStrength 是 Selector.For 接受的锁强度（"UPDATE"、"SHARE"）。
	LockStrength = entsql.LockStrength
	// InsertBuilder builds an INSERT, optionally with an upsert clause.
	// InsertBuilder 构造 INSERT，可带 upsert 子句。
	InsertBuilder = entsql.InsertBuilder
	// UpdateBuilder builds an UPDATE.
	// UpdateBuilder 构造 UPDATE。
	UpdateBuilder = entsql.UpdateBuilder
	// UpdateSet is the new-values side of a resolving upsert.
	// UpdateSet 是 upsert 冲突时的「新值」侧。
	UpdateSet = entsql.UpdateSet
	// DeleteBuilder builds a DELETE.
	// DeleteBuilder 构造 DELETE。
	DeleteBuilder = entsql.DeleteBuilder
	// ConflictOption configures an upsert conflict clause.
	// ConflictOption 配置 upsert 的冲突子句。
	ConflictOption = entsql.ConflictOption
	// WindowBuilder builds a window specification.
	// WindowBuilder 构造窗口定义。
	WindowBuilder = entsql.WindowBuilder
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

// Raw wraps a SQL fragment that carries no arguments; use Expr when it has any.
// Raw 包装一段不带参数的 SQL 片段；带参数时用 Expr。
func Raw(s string) Querier { return entsql.Raw(s) }

// Window builds a window specification for AppendSelectExprAs; RowNumber is the
// common ROW_NUMBER() OVER (...) case, spelled out so it does not have to be a
// hand-written string.
// Window 构造窗口定义，交给 AppendSelectExprAs；RowNumber 是最常见的
// ROW_NUMBER() OVER (...) 写法，不必再手写字符串。
func Window(fn func(*Builder)) *WindowBuilder { return entsql.Window(fn) }

// RowNumber returns ROW_NUMBER() OVER (...).
// RowNumber 返回 ROW_NUMBER() OVER (...)。
func RowNumber() *WindowBuilder { return entsql.RowNumber() }

// Write statements.
//
// These are here because the repository needs them for what gorm's chain cannot
// express — a batch upsert with computed new values, an UPDATE whose SET reads
// the row, a DELETE with a correlated subquery. They are not the default write
// path: repositories keep using gorm, and these only earn their place when the
// statement itself is the hard part.
//
// Execution is the same as for a SELECT: Build, then db.Raw / db.Exec on a handle
// from DBConnect, so the statement joins the ambient transaction and is logged
// like any other.
// 写语句。
//
// 放在这里是因为仓储确实有 gorm 链式表达不了的情况——批量 upsert 且新值要计算、
// SET 里要读本行、DELETE 带关联子查询。它们不是默认写路径：仓储仍走 gorm，只有语句本身
// 才是难点时才用。
//
// 执行与 SELECT 一致：Build 之后交给 db.Raw / db.Exec，句柄来自 DBConnect，因此语句
// 会加入当前事务并被照常记录。

// Insert starts an INSERT statement. OnConflict only renders when the dialect is
// known, so a conflict-resolving insert is written as
// Dialect(DialectMySQL).Insert(...) — see the package doc.
// Insert 开始一条 INSERT 语句。OnConflict 只有在方言已知时才会渲染，因此带冲突处理的
// INSERT 要写成 Dialect(DialectMySQL).Insert(...)——见包注释。
func Insert(table string) *InsertBuilder { return entsql.Insert(table) }

// Update starts an UPDATE statement.
// Update 开始一条 UPDATE 语句。
func Update(table string) *UpdateBuilder { return entsql.Update(table) }

// Delete starts a DELETE statement. Without Where it deletes every row, which is
// why the builder keeps the predicate explicit.
// Delete 开始一条 DELETE 语句。不带 Where 会删除全表，因此条件必须显式给出。
func Delete(table string) *DeleteBuilder { return entsql.Delete(table) }

// Conflict options for InsertBuilder.OnConflict.
// InsertBuilder.OnConflict 的冲突选项。
func ConflictColumns(names ...string) ConflictOption { return entsql.ConflictColumns(names...) }

// ConflictConstraint targets a named constraint instead of columns.
// ConflictConstraint 指定约束名而不是列。
func ConflictConstraint(name string) ConflictOption { return entsql.ConflictConstraint(name) }

// ConflictWhere limits which conflicting rows are handled.
// ConflictWhere 限定哪些冲突行会被处理。
func ConflictWhere(p *Predicate) ConflictOption { return entsql.ConflictWhere(p) }

// UpdateWhere adds a WHERE to the DO UPDATE part of an upsert.
// UpdateWhere 给 upsert 的 DO UPDATE 部分加 WHERE。
func UpdateWhere(p *Predicate) ConflictOption { return entsql.UpdateWhere(p) }

// DoNothing turns the conflict into DO NOTHING.
// DoNothing 把冲突处理变成 DO NOTHING。
func DoNothing() ConflictOption { return entsql.DoNothing() }

// ResolveWithIgnore keeps the existing row on conflict.
// ResolveWithIgnore 冲突时保留已有行。
func ResolveWithIgnore() ConflictOption { return entsql.ResolveWithIgnore() }

// ResolveWithNewValues overwrites with the values proposed by the INSERT.
// ResolveWithNewValues 用 INSERT 提供的新值覆盖。
func ResolveWithNewValues() ConflictOption { return entsql.ResolveWithNewValues() }

// ResolveWith builds the new-values side by hand, for "col = col + excluded.col".
// ResolveWith 手写「新值」侧，用于 col = col + excluded.col 这类场景。
func ResolveWith(fn func(*UpdateSet)) ConflictOption { return entsql.ResolveWith(fn) }

// Column expressions and aggregates. Each renders a SQL fragment and returns it as
// a string, so it can be passed to Select, SelectExpr, OrderBy or a comparison —
// the same shape Count has.
// 列表达式与聚合。每个都渲染 SQL 片段并以字符串返回，可以直接交给 Select、
// SelectExpr、OrderBy 或比较——与 Count 的形态一致。
func Count(ident string) string { return entsql.Count(ident) }

// Sum wraps the identifier with SUM(...).
// Sum 用 SUM(...) 包住标识符。
func Sum(ident string) string { return entsql.Sum(ident) }

// Max wraps the identifier with MAX(...).
// Max 用 MAX(...) 包住标识符。
func Max(ident string) string { return entsql.Max(ident) }

// Min wraps the identifier with MIN(...).
// Min 用 MIN(...) 包住标识符。
func Min(ident string) string { return entsql.Min(ident) }

// Avg wraps the identifier with AVG(...).
// Avg 用 AVG(...) 包住标识符。
func Avg(ident string) string { return entsql.Avg(ident) }

// Lower wraps the identifier with LOWER(...).
// Lower 用 LOWER(...) 包住标识符。
func Lower(ident string) string { return entsql.Lower(ident) }

// As aliases a column or expression: `a` AS `b`.
// As 给列或表达式起别名：`a` AS `b`。
func As(ident string, as string) string { return entsql.As(ident, as) }

// Distinct prefixes the columns with DISTINCT.
// Distinct 给列加上 DISTINCT。
func Distinct(idents ...string) string { return entsql.Distinct(idents...) }

// Asc and Desc render an ORDER BY term.
// Asc 与 Desc 渲染 ORDER BY 的一项。
func Asc(column string) string { return entsql.Asc(column) }

// Desc renders a descending ORDER BY term.
// Desc 渲染降序的 ORDER BY 项。
func Desc(column string) string { return entsql.Desc(column) }

// DescExpr is Desc for an expression rather than a column name.
// DescExpr 是 Desc 的表达式版本，入参不是列名。
func DescExpr(x Querier) Querier { return entsql.DescExpr(x) }

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

// InSlice is In for a typed slice, which is the shape a filter arrives in: a
// []string from a request cannot be spread into In's ...any, and passing it
// whole compiles but yields one argument that holds a slice — it only works if
// gorm later re-expands it, and under the PostgreSQL dialect it also throws the
// placeholder numbering off.
//
// An empty slice keeps In's own behaviour (no arguments, which renders FALSE), so
// an empty filter still means "match nothing". Where empty should instead mean
// "do not filter", skip the predicate at the call site — see the README section
// on conditional building.
// InSlice 是 In 的带类型 slice 版本，也是过滤条件实际出现的形态：请求里的 []string 摊不进
// In 的 ...any，整个传进去虽然能编译，却是一个装着 slice 的参数——只有在 gorm 之后又把它
// 展开时才成立，而在 PostgreSQL 方言下还会打乱占位符编号。
//
// 空 slice 沿用 In 自己的行为（无参数，渲染成 FALSE），空过滤因此仍然表示「匹配零行」。
// 若空过滤应当表示「不筛」，就在调用处跳过这个谓词——见 README 的条件拼接一节。
func InSlice[T any](col string, vals []T) *Predicate {
	return entsql.In(col, spread(vals)...)
}

// NotInSlice is the negated InSlice. An empty slice renders NOT (FALSE), matching
// NotIn with no arguments: "exclude nothing".
// NotInSlice 是 InSlice 的否定。空 slice 渲染 NOT (FALSE)，与不带参数的 NotIn 一致：
// 「不排除任何行」。
func NotInSlice[T any](col string, vals []T) *Predicate {
	return entsql.NotIn(col, spread(vals)...)
}

// spread boxes a typed slice into the []any the upstream builders take, and keeps
// nil rather than an empty slice for the empty case, so the upstream branch for
// "no arguments" stays the single definition of what an empty list matches.
// spread 把带类型的 slice 装箱成上游构造器接收的 []any；空的情况保持 nil 而不是空 slice，
// 让上游「无参数」的那个分支成为「空列表匹配什么」的唯一定义。
func spread[T any](vals []T) []any {
	if len(vals) == 0 {
		return nil
	}
	args := make([]any, len(vals))
	for i, v := range vals {
		args[i] = v
	}
	return args
}

// IsNull and NotNull test for NULL.
// IsNull 与 NotNull 判断 NULL。
func IsNull(col string) *Predicate { return entsql.IsNull(col) }

// NotNull tests for a non-NULL value.
// NotNull 判断非 NULL。
func NotNull(col string) *Predicate { return entsql.NotNull(col) }

// Like matches a column against a pattern; the caller supplies the % signs.
// Like 用模式匹配列；百分号由调用方自己给出。
func Like(col, pattern string) *Predicate { return entsql.Like(col, pattern) }

// HasPrefix, HasSuffix and Contains build the LIKE pattern for you and escape the
// value first, so a % or _ inside it is matched literally instead of turning into
// a wildcard. Hand-writing Like("%"+s+"%") gets that wrong for exactly the inputs
// a search box receives.
// HasPrefix、HasSuffix 与 Contains 替你拼 LIKE 模式，并先转义取值——值里的 % 或 _
// 会按字面匹配，不会变成通配符。手写 Like("%"+s+"%") 恰恰在搜索框最常收到的输入上出错。
func HasPrefix(col, prefix string) *Predicate { return entsql.HasPrefix(col, prefix) }

// HasSuffix matches a suffix.
// HasSuffix 匹配后缀。
func HasSuffix(col, suffix string) *Predicate { return entsql.HasSuffix(col, suffix) }

// Contains matches a substring.
// Contains 匹配子串。
func Contains(col, sub string) *Predicate { return entsql.Contains(col, sub) }

// EqualFold and ContainsFold compare case-insensitively. Their SQL differs per
// dialect (MySQL compares with a case-insensitive collation, PostgreSQL uses
// ILIKE, SQLite wraps LOWER), which is the reason to call them instead of
// hand-writing a LIKE that only works on one database.
// EqualFold 与 ContainsFold 做大小写不敏感的比较。两者的 SQL 因方言而异（MySQL 用
// 不区分大小写的排序规则、PostgreSQL 用 ILIKE、SQLite 套 LOWER），这正是要调用它们
// 而不是手写一条只在某个库上成立 LIKE 的原因。
func EqualFold(col, sub string) *Predicate { return entsql.EqualFold(col, sub) }

// ContainsFold matches a substring case-insensitively.
// ContainsFold 大小写不敏感地匹配子串。
func ContainsFold(col, sub string) *Predicate { return entsql.ContainsFold(col, sub) }

// P is the escape hatch: it takes callbacks on the underlying Builder, for a
// predicate that has no helper. ExprP is the same for a SQL fragment with its own
// arguments. Both are the last resort — a helper that exists is always better,
// because a callback writes SQL the compiler cannot check.
// P 是逃生口：它接收作用于底层 Builder 的回调，用于没有对应辅助函数的谓词。ExprP 是
// 同一件事的「片段 + 参数」版本。两者都是最后手段——有辅助函数就用辅助函数，因为回调里
// 写的 SQL 编译器检查不到。
func P(fns ...func(*Builder)) *Predicate { return entsql.P(fns...) }

// ExprP builds a predicate from a SQL fragment and its arguments.
// ExprP 用一段 SQL 片段及其参数构造谓词。
func ExprP(expr string, args ...any) *Predicate { return entsql.ExprP(expr, args...) }

// False renders a predicate that matches nothing. It is how an empty filter list
// is expressed: And() with no arguments renders no condition at all while the
// selector still emits WHERE, so the statement comes out invalid rather than
// narrowed to zero rows.
// False 渲染一个不匹配任何行的谓词。空过滤列表就用它表达：不带参数的 And() 不渲染任何
// 条件，而 Selector 仍会输出 WHERE，结果是语句无效，而不是收窄到零行。
func False() *Predicate { return entsql.False() }

// IsTrue and IsFalse compare a column with TRUE / FALSE.
// IsTrue 与 IsFalse 把列与 TRUE / FALSE 比较。
func IsTrue(col string) *Predicate { return entsql.IsTrue(col) }

// IsFalse compares a column with FALSE.
// IsFalse 把列与 FALSE 比较。
func IsFalse(col string) *Predicate { return entsql.IsFalse(col) }

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

// ColumnsLT, ColumnsLTE, ColumnsGT and ColumnsGTE are the ordered variants, for
// comparing one column with another instead of with a value.
// ColumnsLT、ColumnsLTE、ColumnsGT、ColumnsGTE 是有序版本，用于两个列之间而不是列与值
// 之间的比较。
func ColumnsLT(col1, col2 string) *Predicate { return entsql.ColumnsLT(col1, col2) }

// ColumnsLTE compares two columns with <=.
// ColumnsLTE 用 <= 比较两个列。
func ColumnsLTE(col1, col2 string) *Predicate { return entsql.ColumnsLTE(col1, col2) }

// ColumnsGT compares two columns with >.
// ColumnsGT 用 > 比较两个列。
func ColumnsGT(col1, col2 string) *Predicate { return entsql.ColumnsGT(col1, col2) }

// ColumnsGTE compares two columns with >=.
// ColumnsGTE 用 >= 比较两个列。
func ColumnsGTE(col1, col2 string) *Predicate { return entsql.ColumnsGTE(col1, col2) }

// CompositeGT and CompositeLT compare row values — (a, b) > (?, ?) — which is what
// keyset pagination needs and what a set of ANDed comparisons cannot express:
// (a, b) > (1, 2) is not a > 1 AND b > 2.
// CompositeGT 与 CompositeLT 比较行值——(a, b) > (?, ?)——这是键集分页需要、而一组用 AND
// 连接的比较表达不了的：(a, b) > (1, 2) 并不等于 a > 1 AND b > 2。
func CompositeGT(columns []string, args ...any) *Predicate {
	return entsql.CompositeGT(columns, args...)
}

// CompositeLT is the less-than form of CompositeGT.
// CompositeLT 是 CompositeGT 的小于版本。
func CompositeLT(columns []string, args ...any) *Predicate {
	return entsql.CompositeLT(columns, args...)
}

// Row lock options. A lock only holds until the surrounding transaction ends, so
// a locking statement must run on a handle that is inside one.
// 行锁选项。锁只持有到事务结束，因此加锁语句必须跑在事务内的句柄上。
func WithLockAction(action LockAction) LockOption { return entsql.WithLockAction(action) }

// WithLockTables scopes the lock to the named tables.
// WithLockTables 把锁限定到指定表。
func WithLockTables(tables ...string) LockOption { return entsql.WithLockTables(tables...) }

// WithLockClause passes a raw lock clause for what the options above cannot
// express — locking a table alias, for instance. It is the last resort, and it is
// not portable: the caller is writing the clause the database expects.
// WithLockClause 传入裸锁子句，用于上面两个选项表达不了的场景——例如锁定表别名。这是
// 最后手段，且不可移植：调用方写的就是数据库期望的那段子句。
func WithLockClause(clause string) LockOption { return entsql.WithLockClause(clause) }

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
