package sqlbuilder_test

import (
	"context"
	"strings"
	"testing"

	"entgo.io/ent/dialect"

	"github.com/bingcool/gofy/support/frameworkcontext"
	"github.com/bingcool/gofy/support/sqlbuilder"
	"github.com/bingcool/gofy/support/tenant"
)

// TestBuildDynamicPredicate pins the shape gorm receives from Where: one
// parenthesised condition per predicate, args in the order the conditions were
// added. The OR group is the case that used to force nested db.Where calls.
// TestBuildDynamicPredicate 固定交给 gorm Where 的形态：每个谓词一段带括号的条件，
// 参数按条件加入的顺序排列。OR 分组正是过去必须嵌套 db.Where 才能表达的那一类。
func TestBuildDynamicPredicate(t *testing.T) {
	orders := sqlbuilder.Table("orders")

	stmt, args, err := sqlbuilder.Build(sqlbuilder.And(
		sqlbuilder.EQ(orders.C("user_id"), 7),
		sqlbuilder.EQ(orders.C("status"), "paid"),
		sqlbuilder.Or(
			sqlbuilder.NotNull(orders.C("owner_id")),
			sqlbuilder.EQ(orders.C("is_public"), 1),
		),
	))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	const want = "`orders`.`user_id` = ? AND `orders`.`status` = ? AND " +
		"(`orders`.`owner_id` IS NOT NULL OR `orders`.`is_public` = ?)"
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 3 || args[0] != 7 || args[1] != "paid" || args[2] != 1 {
		t.Fatalf("args = %#v, want [7 paid 1]", args)
	}
}

// TestInSlice covers the filter shape a request actually produces. A []string
// cannot be spread into In's ...any, and passing it whole is the silent trap: it
// compiles, and the statement carries one argument holding a slice, which only
// survives because gorm re-expands it later. The empty cases keep the upstream
// behaviour: In with no arguments renders FALSE, NotIn renders NOT FALSE.
// TestInSlice 覆盖请求真正产生的过滤形态。[]string 摊不进 In 的 ...any，整个传进去则是
// 静默的坑：能编译，语句里是一个装着 slice 的参数，只有靠 gorm 之后重新展开才成立。两条空
// 分支保持上游行为：In 无参数渲染 FALSE，NotIn 渲染 NOT FALSE。
func TestInSlice(t *testing.T) {
	orders := sqlbuilder.Table("orders")

	stmt, args, err := sqlbuilder.Build(
		sqlbuilder.Select(orders.C("id")).From(orders).
			Where(sqlbuilder.InSlice(orders.C("status"), []string{"paid", "shipped"})),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const want = "SELECT `orders`.`id` FROM `orders` WHERE `orders`.`status` IN (?, ?)"
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 2 || args[0] != "paid" || args[1] != "shipped" {
		t.Fatalf("args = %#v, want [paid shipped]", args)
	}

	// An empty filter narrows to zero rows instead of disappearing.
	stmt, _, err = sqlbuilder.Build(sqlbuilder.InSlice[string](orders.C("status"), nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stmt != "FALSE" {
		t.Fatalf("empty InSlice rendered %q, want FALSE", stmt)
	}

	stmt, _, err = sqlbuilder.Build(sqlbuilder.NotInSlice[int64](orders.C("id"), nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stmt != "NOT (FALSE)" {
		t.Fatalf("empty NotInSlice rendered %q, want NOT (FALSE)", stmt)
	}
}

// TestConditionalWhere is the shape filters should be written in: start the
// statement, add a clause only when the parameter carries a value, and let
// repeated Where calls AND themselves — no outer And wrapper, no empty predicate.
// The last case pins why the outer And is dangerous: Where(And()) leaves a bare
// WHERE behind and produces a statement that cannot run.
// TestConditionalWhere 是过滤条件应有的写法：先建语句，参数有值时才加子句，多次 Where
// 之间自动 AND——不需要外层 And，也不该挂空谓词。最后一段固定外层 And 的危险之处：
// Where(And()) 留下一个光秃秃的 WHERE，语句根本跑不了。
func TestConditionalWhere(t *testing.T) {
	orders := sqlbuilder.Table("orders")

	build := func(status []string, userID int64, keyword string) (string, []any) {
		sel := sqlbuilder.Select(orders.C("id"), orders.C("user_id")).From(orders)
		if len(status) > 0 {
			sel = sel.Where(sqlbuilder.InSlice(orders.C("status"), status))
		}
		if userID > 0 {
			sel = sel.Where(sqlbuilder.GT(orders.C("user_id"), userID))
		}
		if keyword != "" {
			sel = sel.Where(sqlbuilder.Contains(orders.C("name"), keyword))
		}
		stmt, args, err := sqlbuilder.Build(sel)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return stmt, args
	}

	stmt, args := build([]string{"paid"}, 7, "")
	const want = "SELECT `orders`.`id`, `orders`.`user_id` FROM `orders` " +
		"WHERE `orders`.`status` IN (?) AND `orders`.`user_id` > ?"
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 2 || args[0] != "paid" || args[1] != int64(7) {
		t.Fatalf("args = %#v, want [paid 7]", args)
	}

	// No parameter set: the statement keeps no WHERE at all.
	stmt, args = build(nil, 0, "")
	if stmt != "SELECT `orders`.`id`, `orders`.`user_id` FROM `orders`" || len(args) != 0 {
		t.Fatalf("unfiltered stmt =\n%s\nargs = %#v", stmt, args)
	}

	// The trap: an outer And with nothing in it is not "no filter".
	broken, _, err := sqlbuilder.Build(
		sqlbuilder.Select(orders.C("id")).From(orders).Where(sqlbuilder.And()),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.HasSuffix(broken, " WHERE ") {
		t.Fatalf("Where(And()) rendered %q, expected the documented bare WHERE", broken)
	}
}

// TestBuildCTE covers the capability gorm has no clause for: gorm's clause.With
// is an empty struct with no Build method, so this statement could only be a
// hand-written Raw string before. The args order (CTE first, then HAVING) is
// maintained by the builder, which is the point.
// TestBuildCTE 覆盖 gorm 完全没有对应 clause 的能力：gorm 的 clause.With 是个没有
// Build 方法的空结构，这条语句以前只能手写 Raw 字符串。参数顺序（先 CTE，后 HAVING）
// 由 builder 维护，这正是引入它的理由。
func TestBuildCTE(t *testing.T) {
	orders := sqlbuilder.Table("orders")

	paid := sqlbuilder.Select(orders.C("id"), orders.C("user_id")).
		From(orders).
		Where(sqlbuilder.EQ(orders.C("status"), "paid"))
	with := sqlbuilder.With("paid_orders").As(paid)

	stmt, args, err := sqlbuilder.Build(
		sqlbuilder.Select(with.C("user_id"), sqlbuilder.Count("*")).
			From(with).
			GroupBy(with.C("user_id")).
			Having(sqlbuilder.GT(sqlbuilder.Count("*"), 2)).
			Prefix(with),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	const want = "WITH `paid_orders` AS (SELECT `orders`.`id`, `orders`.`user_id` FROM `orders` " +
		"WHERE `orders`.`status` = ?) SELECT `paid_orders`.`user_id`, COUNT(*) FROM `paid_orders` " +
		"GROUP BY `paid_orders`.`user_id` HAVING COUNT(*) > ?"
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 2 || args[0] != "paid" || args[1] != 2 {
		t.Fatalf("args = %#v, want [paid 2]", args)
	}
}

// TestBuildUnion: UNION is the other clause gorm lacks — there is no
// clause.Union type at all.
// TestBuildUnion：UNION 是 gorm 缺的另一个 clause——它根本没有 Union 类型。
func TestBuildUnion(t *testing.T) {
	orders := sqlbuilder.Table("orders")
	items := sqlbuilder.Table("order_items")

	stmt, args, err := sqlbuilder.Build(
		sqlbuilder.Select(orders.C("id")).From(orders).
			Where(sqlbuilder.EQ(orders.C("status"), "paid")).
			Union(sqlbuilder.Select(items.C("order_id")).From(items)),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	const want = "SELECT `orders`.`id` FROM `orders` WHERE `orders`.`status` = ? " +
		"UNION SELECT `order_items`.`order_id` FROM `order_items`"
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 1 || args[0] != "paid" {
		t.Fatalf("args = %#v, want [paid]", args)
	}
}

// TestBuildNotExists pins the correlated subquery used by the query-layer
// example: the column comparison is rendered as `a` = `b`, not as a placeholder,
// so a NOT EXISTS built this way contributes no args of its own.
// TestBuildNotExists 固定 query 层示例使用的关联子查询：列比较渲染成 `a` = `b` 而不是
// 占位符，因此这样构造的 NOT EXISTS 自身不贡献参数。
func TestBuildNotExists(t *testing.T) {
	products := sqlbuilder.Table("products")
	items := sqlbuilder.Table("order_items")

	unused := sqlbuilder.SelectExpr(sqlbuilder.Expr("1")).From(items).
		Where(sqlbuilder.ColumnsEQ(items.C("product_id"), products.C("id")))
	stmt, args, err := sqlbuilder.Build(
		sqlbuilder.Select(products.C("id")).From(products).
			Where(sqlbuilder.NotExists(unused)).
			OrderBy(products.C("id")),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	const want = "SELECT `products`.`id` FROM `products` WHERE NOT EXISTS " +
		"(SELECT 1 FROM `order_items` WHERE `order_items`.`product_id` = `products`.`id`) " +
		"ORDER BY `products`.`id`"
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v, want none", args)
	}
}

// TestBuildReportsConstructionError is the reason Build exists. ForUpdate under
// the SQLite dialect records an error instead of panicking or returning it, so a
// caller using Query() directly would execute a statement that was never valid.
// Build turns that into a returned error and withholds the half-built statement.
// TestBuildReportsConstructionError 就是 Build 存在的理由。SQLite 方言下的 ForUpdate
// 只把错误记进 builder（既不 panic 也不返回），直接用 Query() 的调用方会去执行一条本来
// 就无效的语句。Build 把它变成返回错误，并且不交出半成品语句。
func TestBuildReportsConstructionError(t *testing.T) {
	stmt, args, err := sqlbuilder.Build(
		sqlbuilder.Dialect(dialect.SQLite).Select("id").From(sqlbuilder.Table("orders")).
			ForUpdate(sqlbuilder.WithLockAction(sqlbuilder.SkipLocked)),
	)
	if err == nil {
		t.Fatal("Build swallowed the builder error")
	}
	if !strings.HasPrefix(err.Error(), "sqlbuilder: ") {
		t.Fatalf("error = %q, want the sqlbuilder prefix", err)
	}
	if !strings.Contains(err.Error(), "FOR UPDATE") {
		t.Fatalf("error = %q, want the original reason kept", err)
	}
	if stmt != "" || args != nil {
		t.Fatalf("stmt = %q, args = %#v; a failed build must not hand back a statement", stmt, args)
	}
}

// TestJoinAliasIsFixedWhenCIsCalled pins the footgun behind "build tables next to
// the statement": Join assigns t1, t2, ... to a table that has no alias, and C
// bakes whatever alias exists at the moment it is called. Function arguments are
// evaluated before Join runs, so even the one-expression form renders a SELECT
// list pointing at `users` while the FROM clause has renamed it to t1 — a
// statement that fails at run time with "unknown column". An explicit As makes
// the two agree regardless of evaluation order.
// TestJoinAliasIsFixedWhenCIsCalled 固定「表对象与语句一起构造」背后的坑：Join 会给没有
// 别名的表分配 t1、t2…，而 C 在调用那一刻就把当时的别名烘进字符串。函数实参在 Join 之前
// 求值，所以即使写成一条链，SELECT 列表里仍是 `users`，而 FROM 已经把这张表改名为 t1——
// 运行时报「未知列」。显式 As 之后两次取值一致，与求值顺序无关。
func TestJoinAliasIsFixedWhenCIsCalled(t *testing.T) {
	users := sqlbuilder.Table("users")
	before := users.C("name")
	if before != "`users`.`name`" {
		t.Fatalf("C before Join = %q, want `users`.`name`", before)
	}

	stmt, _, err := sqlbuilder.Build(
		sqlbuilder.Select(users.C("id")).From(sqlbuilder.Table("orders")).Join(users),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const wantBroken = "SELECT `users`.`id` FROM `orders` JOIN `users` AS `t1`"
	if stmt != wantBroken {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, wantBroken)
	}

	// After the join the same helper renders the alias, so a statement rebuilt
	// from here happens to be correct — which is exactly why the ordering bug is
	// so easy to miss.
	if after := users.C("id"); after != "`t1`.`id`" {
		t.Fatalf("C after Join = %q, want `t1`.`id`", after)
	}

	aliased := sqlbuilder.Table("users").As("u")
	first := aliased.C("name")
	stmt, _, err = sqlbuilder.Build(
		sqlbuilder.Select(aliased.C("id")).From(sqlbuilder.Table("orders")).Join(aliased),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const wantAliased = "SELECT `u`.`id` FROM `orders` JOIN `users` AS `u`"
	if stmt != wantAliased {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, wantAliased)
	}
	if aliased.C("name") != first {
		t.Fatalf("an explicit alias changed after Join: %q then %q", first, aliased.C("name"))
	}
}

// TestSharedPredicateIsNotATemplate pins what "keep a predicate in a field and
// add conditions per request" actually does. A Predicate carries no operator:
// combining happens in And/Or, which wrap their operands. Calling p.In(...) on an
// existing predicate appends an unseparated fragment to the same stream, so every
// reuse leaves the statement longer and invalid, with the args still accumulating.
// Concurrent reuse never gets that far — Build renders into the predicate's own
// Builder and mayWrap writes the child's depth, which -race reports at mayWrap.
// TestSharedPredicateIsNotATemplate 固定「把谓词存进字段、每次请求往上加条件」的真实后果。
// Predicate 自身不带运算符：组合由 And/Or 完成（它们会包住操作数）。在已有谓词上调用
// p.In(...) 只是往同一个输出流里追加一段没有分隔符的片段，因此每次复用都让语句更长且无效，
// 参数也一起累积。并发复用更早出问题——Build 渲染进谓词自己的 Builder，mayWrap 还会写子谓词
// 的 depth，-race 会在 mayWrap 处报数据竞争。
func TestSharedPredicateIsNotATemplate(t *testing.T) {
	tpl := sqlbuilder.EQ("status", "paid")

	first, firstArgs, err := sqlbuilder.Build(tpl.In("id", 1, 2))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const wantFirst = "`status` = ?`id` IN (?, ?)"
	if first != wantFirst {
		t.Fatalf("first reuse =\n%s\nwant\n%s", first, wantFirst)
	}
	if len(firstArgs) != 3 || firstArgs[0] != "paid" || firstArgs[1] != 1 || firstArgs[2] != 2 {
		t.Fatalf("first args = %#v, want [paid 1 2]", firstArgs)
	}

	second, secondArgs, err := sqlbuilder.Build(tpl.In("id", 3, 4))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const wantSecond = "`status` = ?`id` IN (?, ?)`id` IN (?, ?)"
	if second != wantSecond {
		t.Fatalf("second reuse =\n%s\nwant\n%s", second, wantSecond)
	}
	if len(secondArgs) != 5 {
		t.Fatalf("second args = %#v, want the first call's args still in there", secondArgs)
	}
}

// TestDialectQuoting pins the three quoting behaviours the package doc warns
// about. The SQLite case asserts a defect on purpose: ent renders the column as
// backticks around a double-quoted name, which SQLite reads as one column named
// `"orders"."id"`, so the query fails with "no such column". If upstream ever
// fixes it this test fails, and the warning in the package doc can be dropped.
// TestDialectQuoting 固定包注释里提示的三种引号行为。SQLite 那条是故意断言缺陷：ent 把
// 列渲染成反引号套双引号，SQLite 于是把它当成一个名为 `"orders"."id"` 的列，查询报
// "no such column"。上游修复后这条会失败，届时可以删掉包注释里的警告。
func TestDialectQuoting(t *testing.T) {
	orders := sqlbuilder.Table("orders")

	cases := []struct {
		name  string
		build func() sqlbuilder.Querier
		want  string
	}{
		{
			name:  "default is backtick",
			build: func() sqlbuilder.Querier { return sqlbuilder.Select(orders.C("id")).From(orders) },
			want:  "SELECT `orders`.`id` FROM `orders`",
		},
		{
			name: "postgres switches to double quotes",
			build: func() sqlbuilder.Querier {
				return sqlbuilder.Dialect(sqlbuilder.DialectPostgres).Select(orders.C("id")).From(orders)
			},
			want: `SELECT "orders"."id" FROM "orders"`,
		},
		{
			name: "sqlite must not be passed explicitly",
			build: func() sqlbuilder.Querier {
				return sqlbuilder.Dialect(dialect.SQLite).Select(orders.C("id")).From(orders)
			},
			want: "SELECT `\"orders\".\"id\"` FROM `orders`",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmt, _, err := sqlbuilder.Build(tc.build())
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if stmt != tc.want {
				t.Fatalf("stmt =\n%s\nwant\n%s", stmt, tc.want)
			}
		})
	}
}

// TestTenantScope covers both branches: a request with a tenant gets the equality
// predicate, a request without one gets false and no predicate, so the caller can
// decide whether that is an error or a reason to skip filtering.
// TestTenantScope 覆盖两个分支：带租户的请求拿到等值谓词，不带的拿到 false 与 nil，
// 由调用方决定这是错误还是跳过过滤。
func TestTenantScope(t *testing.T) {
	orders := sqlbuilder.Table("orders")

	ctx := frameworkcontext.WithUser(context.Background(), frameworkcontext.User{ID: "1", TenantID: "t-42"})
	pred, ok := sqlbuilder.TenantScope(ctx, orders)
	if !ok {
		t.Fatal("TenantScope reported no tenant for a request that has one")
	}
	stmt, args, err := sqlbuilder.Build(pred)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const want = "`orders`.`tenant_id` = ?"
	if stmt != want {
		t.Fatalf("stmt = %q, want %q", stmt, want)
	}
	if len(args) != 1 || args[0] != "t-42" {
		t.Fatalf("args = %#v, want [t-42]", args)
	}

	if pred, ok := sqlbuilder.TenantScope(context.Background(), orders); ok || pred != nil {
		t.Fatalf("TenantScope returned (%v, %v) without a tenant", pred, ok)
	}
}

// TestDefaultTenantColumnMatchesTenant: the column name is duplicated instead of
// imported, because support/tenant pulls in gorm and this package must stay free
// of it. This test is what keeps the two from drifting.
// TestDefaultTenantColumnMatchesTenant：列名是复制的而不是 import 来的，
// 因为 support/tenant 会带进 gorm 而本包必须与它无关。防止两边漂移的就是这条测试。
func TestDefaultTenantColumnMatchesTenant(t *testing.T) {
	if sqlbuilder.DefaultTenantColumn != tenant.DefaultColumn {
		t.Fatalf("DefaultTenantColumn = %q, tenant.DefaultColumn = %q",
			sqlbuilder.DefaultTenantColumn, tenant.DefaultColumn)
	}
}

// TestBuildInsertUpsert covers the write statements, which are the reason the
// package is not SELECT-only: a batch upsert with resolved values has no gorm
// chain equivalent. The two dialects render different clauses for the same call,
// which is why the dialect has to be passed explicitly.
// TestBuildInsertUpsert 覆盖写语句——这正是本包不只做 SELECT 的原因：带冲突处理的批量
// upsert 在 gorm 链式里没有等价写法。同一段调用在两种方言下渲染出不同子句，这也是方言必须
// 显式传入的原因。
func TestBuildInsertUpsert(t *testing.T) {
	stmt, args, err := sqlbuilder.Build(
		sqlbuilder.Dialect(sqlbuilder.DialectMySQL).
			Insert("orders").
			Columns("id", "status", "amount").
			Values(1, "paid", 100).
			OnConflict(sqlbuilder.ConflictColumns("id"), sqlbuilder.ResolveWithNewValues()),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const wantMySQL = "INSERT INTO `orders` (`id`, `status`, `amount`) VALUES (?, ?, ?) " +
		"ON DUPLICATE KEY UPDATE `id` = VALUES(`id`), `status` = VALUES(`status`), " +
		"`amount` = VALUES(`amount`)"
	if stmt != wantMySQL {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, wantMySQL)
	}
	if len(args) != 3 || args[0] != 1 || args[1] != "paid" || args[2] != 100 {
		t.Fatalf("args = %#v", args)
	}

	stmt, args, err = sqlbuilder.Build(
		sqlbuilder.Dialect(sqlbuilder.DialectPostgres).
			Insert("orders").
			Columns("id", "status").
			Values(1, "paid").
			OnConflict(sqlbuilder.ConflictColumns("id"), sqlbuilder.DoNothing()),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const wantPG = `INSERT INTO "orders" ("id", "status") VALUES ($1, $2) ON CONFLICT ("id") DO NOTHING`
	if stmt != wantPG {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, wantPG)
	}
	if len(args) != 2 || args[0] != 1 || args[1] != "paid" {
		t.Fatalf("args = %#v", args)
	}
}

// TestUpsertWithoutDialectIsInvalid pins a trap worth knowing before it is hit:
// upstream switches the conflict clause on the dialect and has no default branch,
// so Insert(table) + OnConflict emits no ON DUPLICATE KEY UPDATE / ON CONFLICT at
// all and produces an invalid statement without recording an error. The test fails
// if upstream ever adds a default branch, at which point the warning in the package
// doc can go away.
// TestUpsertWithoutDialectIsInvalid 固定一个踩到就麻烦的坑：上游按方言分支渲染冲突子句，
// 且没有 default 分支，因此 Insert(table) + OnConflict 根本不输出
// ON DUPLICATE KEY UPDATE / ON CONFLICT，产出一条无效语句却不记录错误。上游若补上 default
// 分支这条会失败，届时可以删掉包注释里的警告。
func TestUpsertWithoutDialectIsInvalid(t *testing.T) {
	stmt, _, err := sqlbuilder.Build(
		sqlbuilder.Insert("orders").
			Columns("id", "status").
			Values(1, "paid").
			OnConflict(sqlbuilder.ConflictColumns("id"), sqlbuilder.ResolveWithNewValues()),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.Contains(stmt, "ON DUPLICATE KEY") || strings.Contains(stmt, "ON CONFLICT") {
		t.Fatalf("the conflict clause rendered without a dialect: %s", stmt)
	}
}

// TestBuildUpdateAndDelete: Set writes a value, Add writes a computed one, and
// Where takes the same Predicate tree as a SELECT.
// TestBuildUpdateAndDelete：Set 写值，Add 写计算表达式，Where 接收与 SELECT 相同的谓词树。
func TestBuildUpdateAndDelete(t *testing.T) {
	stmt, args, err := sqlbuilder.Build(
		sqlbuilder.Update("orders").
			Set("status", "done").
			Add("amount", 5).
			Where(sqlbuilder.EQ("id", 7)),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const wantUpdate = "UPDATE `orders` SET `status` = ?, " +
		"`amount` = COALESCE(`orders`.`amount`, 0) + ? WHERE `id` = ?"
	if stmt != wantUpdate {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, wantUpdate)
	}
	if len(args) != 3 || args[0] != "done" || args[1] != 5 || args[2] != 7 {
		t.Fatalf("args = %#v", args)
	}

	stmt, args, err = sqlbuilder.Build(
		sqlbuilder.Delete("orders").Where(sqlbuilder.EQ("status", "cancelled")),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const wantDelete = "DELETE FROM `orders` WHERE `status` = ?"
	if stmt != wantDelete {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, wantDelete)
	}
	if len(args) != 1 || args[0] != "cancelled" {
		t.Fatalf("args = %#v", args)
	}
}

// TestBuildAggregatesAndOrder renders the report shape the query layer writes by
// hand today: aggregates in the select list, an alias, DISTINCT and an ordered
// ORDER BY.
// TestBuildAggregatesAndOrder 渲染 query 层目前手写的那类报表：选择列表里的聚合、
// 别名、DISTINCT，以及带方向的 ORDER BY。
func TestBuildAggregatesAndOrder(t *testing.T) {
	orders := sqlbuilder.Table("orders")

	stmt, args, err := sqlbuilder.Build(
		sqlbuilder.Select(
			orders.C("user_id"),
			sqlbuilder.Sum("amount"),
			sqlbuilder.Max(orders.C("id")),
			sqlbuilder.Avg(orders.C("amount")),
			sqlbuilder.As(sqlbuilder.Count("*"), "n"),
			sqlbuilder.Distinct(orders.C("status")),
		).
			From(orders).
			GroupBy(orders.C("user_id")).
			OrderBy(sqlbuilder.Desc("amount"), sqlbuilder.Asc(orders.C("user_id"))),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const want = "SELECT `orders`.`user_id`, SUM(`amount`), MAX(`orders`.`id`), " +
		"AVG(`orders`.`amount`), COUNT(*) AS `n`, DISTINCT `orders`.`status` " +
		"FROM `orders` GROUP BY `orders`.`user_id` " +
		"ORDER BY `amount` DESC, `orders`.`user_id` ASC"
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v, want none", args)
	}
}

// TestBuildWindowRowNumber replaces the hand-written OVER (...) string: the
// window is built from parts, and AppendSelectExprAs gives it a name. Using
// SelectExpr and AppendSelectExprAs with the same expression would emit the
// column twice.
// TestBuildWindowRowNumber 取代手写的 OVER (...) 字符串：窗口由部件拼出来，
// AppendSelectExprAs 负责起名。同一个表达式同时用 SelectExpr 与 AppendSelectExprAs
// 会输出两列。
func TestBuildWindowRowNumber(t *testing.T) {
	orders := sqlbuilder.Table("orders")

	stmt, _, err := sqlbuilder.Build(
		sqlbuilder.Select(orders.C("id")).
			AppendSelectExprAs(sqlbuilder.RowNumber().PartitionBy("user_id").OrderBy("created_at"), "rn").
			From(orders),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const want = "SELECT `orders`.`id`, (ROW_NUMBER() OVER (PARTITION BY `user_id` " +
		"ORDER BY `created_at`)) AS `rn` FROM `orders`"
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
}

// TestBuildEscapedLike: the helper escapes the value before adding wildcards, so a
// search box receiving "50%" does not turn it into a prefix match, and "_" does
// not match any character. EqualFold renders the dialect's own case-folding; under
// the default dialect that is LOWER(...), which works on every database.
// TestBuildEscapedLike：辅助函数先转义取值再加通配符，因此搜索框收到 "50%" 不会变成前缀
// 匹配、"_" 也不会匹配任意字符。EqualFold 渲染方言自己的大小写折叠；默认方言下是
// LOWER(...)，在每个数据库上都成立。
func TestBuildEscapedLike(t *testing.T) {
	stmt, args, err := sqlbuilder.Build(sqlbuilder.And(
		sqlbuilder.HasPrefix("name", "50%"),
		sqlbuilder.Contains("name", "a_b"),
		sqlbuilder.EqualFold("email", "A@B.com"),
	))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const want = "`name` LIKE ? AND `name` LIKE ? AND LOWER(`email`) = ?"
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 3 || args[0] != "50\\%%" || args[1] != "%a\\_b%" || args[2] != "a@b.com" {
		t.Fatalf("args = %#v, want the escaped patterns and a lowered value", args)
	}
}

// TestBuildCompositeKeyset: (a, b) > (?, ?) is not a > ? AND b > ?, which is the
// reason keyset pagination needs a composite predicate rather than two.
// TestBuildCompositeKeyset：(a, b) > (?, ?) 并不等于 a > ? AND b > ?，这就是键集分页
// 需要复合谓词、而不是两个普通比较的原因。
func TestBuildCompositeKeyset(t *testing.T) {
	stmt, args, err := sqlbuilder.Build(
		sqlbuilder.CompositeGT([]string{"created_at", "id"}, "2026-01-01", 100),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const want = "(`created_at`, `id`) > (?, ?)"
	if stmt != want {
		t.Fatalf("stmt = %q, want %q", stmt, want)
	}
	if len(args) != 2 || args[0] != "2026-01-01" || args[1] != 100 {
		t.Fatalf("args = %#v", args)
	}
}

// TestBuildEscapeHatches covers what has no helper: P writes through the builder,
// ExprP takes a fragment with arguments, Raw takes one without, and False is how
// an empty filter list becomes "match nothing" instead of silently widening the
// query.
// TestBuildEscapeHatches 覆盖没有辅助函数的写法：P 直接写 builder，ExprP 接收带参数的
// 片段，Raw 接收不带参数的片段，False 让空过滤列表变成「匹配零行」而不是静默放宽查询。
func TestBuildEscapeHatches(t *testing.T) {
	stmt, args, err := sqlbuilder.Build(sqlbuilder.P(func(b *sqlbuilder.Builder) {
		b.WriteString("`orders`.`id` IN (SELECT id FROM `orders` WHERE `status` = ")
		b.Arg("paid")
		b.WriteString(")")
	}))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const wantP = "`orders`.`id` IN (SELECT id FROM `orders` WHERE `status` = ?)"
	if stmt != wantP {
		t.Fatalf("stmt = %q, want %q", stmt, wantP)
	}
	if len(args) != 1 || args[0] != "paid" {
		t.Fatalf("args = %#v", args)
	}

	stmt, args, err = sqlbuilder.Build(sqlbuilder.ExprP("JSON_CONTAINS(tags, ?)", `["vip"]`))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stmt != "JSON_CONTAINS(tags, ?)" || len(args) != 1 {
		t.Fatalf("stmt = %q, args = %#v", stmt, args)
	}

	// Raw carries no error method, so Build's type assertion is what keeps it
	// from panicking; Expr behaves the same way.
	stmt, _, err = sqlbuilder.Build(sqlbuilder.Raw("NOW()"))
	if err != nil || stmt != "NOW()" {
		t.Fatalf("stmt = %q, err = %v", stmt, err)
	}

	stmt, _, err = sqlbuilder.Build(sqlbuilder.And(sqlbuilder.IsTrue("enabled"), sqlbuilder.False()))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stmt != "`enabled` AND FALSE" {
		t.Fatalf("stmt = %q", stmt)
	}
}

// TestBuildPostgresPlaceholders: the dialect rewrites placeholders as well as
// quotes, and a fragment that carries its own "?" keeps it — the two kinds must
// not be mixed in one statement, because gorm would renumber the "?" from 1.
// TestBuildPostgresPlaceholders：方言除了引号还会改占位符，而自带 "?" 的片段保持不变。
// 一条语句里不能混用两种占位符，否则 gorm 会把 "?" 从 1 重新编号，与已有的 $n 撞车。
func TestBuildPostgresPlaceholders(t *testing.T) {
	orders := sqlbuilder.Table("orders")

	stmt, args, err := sqlbuilder.Build(
		sqlbuilder.Dialect(sqlbuilder.DialectPostgres).
			Select(orders.C("id")).
			From(orders).
			Where(sqlbuilder.And(
				sqlbuilder.EQ(orders.C("status"), "paid"),
				sqlbuilder.In(orders.C("id"), 1, 2),
			)),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const want = `SELECT "orders"."id" FROM "orders" WHERE "orders"."status" = $1 ` +
		`AND "orders"."id" IN ($2, $3)`
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 3 {
		t.Fatalf("args = %#v", args)
	}

	// The mixed form: the builder writes $1 while the fragment keeps its ?.
	mixed, _, err := sqlbuilder.Build(
		sqlbuilder.Dialect(sqlbuilder.DialectPostgres).
			Select(orders.C("id")).
			From(orders).
			Where(sqlbuilder.And(
				sqlbuilder.EQ(orders.C("status"), "paid"),
				sqlbuilder.ExprP("amount > ?", 0),
			)),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(mixed, "$1") || !strings.Contains(mixed, "?") {
		t.Fatalf("the mixed statement did not keep both placeholder kinds: %s", mixed)
	}
}

// TestTableCarriesTheDialectOfTheStatementItJoined pins the second statefulness
// trap: From copies the statement's dialect into the *SelectTable, so a table
// value reused under another dialect renders nested quotes. Build tables next to
// the statement that uses them; if upstream stops mutating the table, this test
// fails and the warning can go.
// TestTableCarriesTheDialectOfTheStatementItJoined 固定第二个有状态的坑：From 会把语句的
// 方言写回 *SelectTable，因此跨方言复用同一个表值会渲染出嵌套引号。表对象应与使用它的语句
// 一起构造；上游若不再写回，这条会失败，届时可以删掉警告。
func TestTableCarriesTheDialectOfTheStatementItJoined(t *testing.T) {
	fresh := sqlbuilder.Table("orders")
	stmt, _, err := sqlbuilder.Build(
		sqlbuilder.Dialect(sqlbuilder.DialectMySQL).Select(fresh.C("id")).From(fresh),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stmt != "SELECT `orders`.`id` FROM `orders`" {
		t.Fatalf("fresh table rendered %q", stmt)
	}

	reused := sqlbuilder.Table("orders")
	if _, _, err := sqlbuilder.Build(
		sqlbuilder.Dialect(sqlbuilder.DialectPostgres).Select(reused.C("id")).From(reused),
	); err != nil {
		t.Fatalf("Build: %v", err)
	}
	stmt, _, err = sqlbuilder.Build(
		sqlbuilder.Dialect(sqlbuilder.DialectMySQL).Select(reused.C("id")).From(reused),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stmt != "SELECT `\"orders\".\"id\"` FROM `orders`" {
		t.Fatalf("reused table rendered %q, expected the documented nested-quote defect", stmt)
	}
}
