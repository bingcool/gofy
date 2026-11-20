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
