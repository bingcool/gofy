package sqlmodel_test

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm/schema"

	"github.com/bingcool/gofy/support/frameworkcontext"
	"github.com/bingcool/gofy/support/sqlbuilder"
	"github.com/bingcool/gofy/support/sqlmodel"
	"github.com/bingcool/gofy/support/tenant"
)

// order declares the tenant column; audit does not.
type order struct {
	ID       uint64
	TenantID string
	Status   string
}

func (order) TableName() string { return "orders" }

type audit struct {
	ID     uint64
	Action string
}

func (audit) TableName() string { return "audit_entries" }

// orgOrder is tenant-scoped through a column that is not called tenant_id.
type orgOrder struct {
	ID    uint64
	OrgID string
}

func (orgOrder) TableName() string { return "org_orders" }

func build(t *testing.T, sel *sqlbuilder.Selector) (string, []any) {
	t.Helper()
	stmt, args, err := sqlbuilder.Build(sel)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return stmt, args
}

// TestSelectWithoutTenantColumn: a model that does not declare the column gets no
// condition and needs no tenant, so background jobs over such tables keep working.
// TestSelectWithoutTenantColumn：不声明租户列的模型不会附加条件，也不需要租户，
// 因此针对这类表的后台任务照常工作。
func TestSelectWithoutTenantColumn(t *testing.T) {
	sel, err := sqlmodel.Select(context.Background(), audit{}, "id", "action")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	stmt, args := build(t, sel)
	const want = "SELECT `audit_entries`.`id`, `audit_entries`.`action` FROM `audit_entries`"
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v, want none", args)
	}
}

// TestSelectAppliesTenantScope: the table name comes from TableName(), the
// columns are qualified with it, and the tenant condition is already attached.
// TestSelectAppliesTenantScope：表名取自 TableName()，列由它限定，租户条件已经挂好。
func TestSelectAppliesTenantScope(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), "t-42")
	sel, err := sqlmodel.Select(ctx, order{}, "id", "status")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	stmt, args := build(t, sel)
	const want = "SELECT `orders`.`id`, `orders`.`status` FROM `orders` " +
		"WHERE `orders`.`tenant_id` = ?"
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 1 || args[0] != "t-42" {
		t.Fatalf("args = %#v, want [t-42]", args)
	}
}

// TestSelectReadsTenantFromIdentity: with no explicit override the tenant comes
// from the same identity source the plugin uses.
// TestSelectReadsTenantFromIdentity：没有显式覆盖时，租户取自插件所用的同一身份来源。
func TestSelectReadsTenantFromIdentity(t *testing.T) {
	ctx := frameworkcontext.WithUser(context.Background(), frameworkcontext.User{ID: "1", TenantID: "t-7"})
	sel, err := sqlmodel.Select(ctx, order{}, "id")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	_, args := build(t, sel)
	if len(args) != 1 || args[0] != "t-7" {
		t.Fatalf("args = %#v, want [t-7]", args)
	}
}

// TestSelectFailsClosed: a tenant-scoped model with no tenant is an error, not an
// unfiltered query. This is the difference between a bug and a cross-tenant read.
// TestSelectFailsClosed：带租户列的模型在无租户时返回错误，而不是退化成不带过滤的
// 查询。这正是「一个 bug」与「跨租户读取」的差别。
func TestSelectFailsClosed(t *testing.T) {
	sel, err := sqlmodel.Select(context.Background(), order{}, "id")
	if !errors.Is(err, tenant.ErrTenantRequired) {
		t.Fatalf("err = %v, want tenant.ErrTenantRequired", err)
	}
	if sel != nil {
		t.Fatalf("Select returned a statement alongside the error: %v", sel)
	}
}

// TestSelectRespectsBypass: tenant.Without wins over the column check, the same
// order the plugin applies, so a maintenance script reads across tenants.
// TestSelectRespectsBypass：tenant.Without 压过列判定，与插件顺序一致，运维脚本因此
// 可以跨租户读取。
func TestSelectRespectsBypass(t *testing.T) {
	ctx := tenant.Without(context.Background())
	sel, err := sqlmodel.Select(ctx, order{}, "id")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	stmt, _ := build(t, sel)
	const want = "SELECT `orders`.`id` FROM `orders`"
	if stmt != want {
		t.Fatalf("stmt = %q, want %q", stmt, want)
	}
}

// TestSelectUsesConfiguredColumn: a project that renamed the tenant column passes
// the same Handler it gave the plugin, and the two paths keep looking at one
// column.
// TestSelectUsesConfiguredColumn：改过租户列名的项目把给插件的同一个 Handler 传进来，
// 两条路径因此盯的是同一列。
func TestSelectUsesConfiguredColumn(t *testing.T) {
	r := sqlmodel.New(sqlmodel.Options{
		Handler: tenant.FuncHandler{ColumnName: "org_id"},
	})
	ctx := tenant.WithTenant(context.Background(), "org-1")
	sel, err := r.Select(ctx, orgOrder{}, "id")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	stmt, args := build(t, sel)
	const want = "SELECT `org_orders`.`id` FROM `org_orders` WHERE `org_orders`.`org_id` = ?"
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 1 || args[0] != "org-1" {
		t.Fatalf("args = %#v, want [org-1]", args)
	}
}

// prefixedNamer renames every column, standing in for a project with a custom
// NamingStrategy. Detection has to follow the namer, because that is what decides
// which column TenantID maps to.
// prefixedNamer 给所有列改名，代表配置了自定义 NamingStrategy 的项目。列判定必须跟着
// namer 走，因为是它决定 TenantID 映射到哪一列。
type prefixedNamer struct {
	schema.NamingStrategy
}

func (n prefixedNamer) ColumnName(table, column string) string {
	return "x_" + n.NamingStrategy.ColumnName(table, column)
}

// TestSelectFollowsNamer pins the Namer contract: schema parsing uses the namer
// the connection was built with, so the column is found under its real name.
// TestSelectFollowsNamer 固定 Namer 契约：schema 解析使用构建连接时的 namer，因此能在
// 真实列名下找到租户列。
func TestSelectFollowsNamer(t *testing.T) {
	r := sqlmodel.New(sqlmodel.Options{
		Handler: tenant.FuncHandler{ColumnName: "x_tenant_id"},
		Namer:   prefixedNamer{},
	})
	ctx := tenant.WithTenant(context.Background(), "t-1")
	sel, err := r.Select(ctx, order{}, "id")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	stmt, args := build(t, sel)
	const want = "SELECT `orders`.`id` FROM `orders` WHERE `orders`.`x_tenant_id` = ?"
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 1 || args[0] != "t-1" {
		t.Fatalf("args = %#v", args)
	}
}

// TestSelectPostgresDialect: the dialect reaches both the qualified columns and
// the tenant predicate, including the placeholder numbering.
// TestSelectPostgresDialect：方言同时作用于限定列名与租户谓词，占位符编号也在内。
func TestSelectPostgresDialect(t *testing.T) {
	r := sqlmodel.New(sqlmodel.Options{Dialect: sqlbuilder.DialectPostgres})
	ctx := tenant.WithTenant(context.Background(), "t-9")
	sel, err := r.Select(ctx, order{}, "id")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	stmt, args := build(t, sel)
	const want = `SELECT "orders"."id" FROM "orders" WHERE "orders"."tenant_id" = $1`
	if stmt != want {
		t.Fatalf("stmt =\n%s\nwant\n%s", stmt, want)
	}
	if len(args) != 1 || args[0] != "t-9" {
		t.Fatalf("args = %#v", args)
	}
}

// TestSelectStar: no columns means `table.*`, not an empty select list.
// TestSelectStar：不传列表示 `table.*`，而不是空的列清单。
func TestSelectStar(t *testing.T) {
	ctx := tenant.Without(context.Background())
	sel, err := sqlmodel.Select(ctx, order{})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	stmt, _ := build(t, sel)
	const want = "SELECT `orders`.* FROM `orders`"
	if stmt != want {
		t.Fatalf("stmt = %q, want %q", stmt, want)
	}
}

// TestTableReturnsPredicate covers the join/CTE path: the predicate comes back
// for the caller to attach, and it is already qualified for the table.
// TestTableReturnsPredicate 覆盖 join/CTE 路径：谓词返回给调用方自己挂，且已按表限定。
func TestTableReturnsPredicate(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), "t-3")
	ref, pred, err := sqlmodel.Default.Table(ctx, order{})
	if err != nil {
		t.Fatalf("Table: %v", err)
	}
	if ref.C("id") != "`orders`.`id`" {
		t.Fatalf("ref.C(id) = %q", ref.C("id"))
	}
	if pred == nil {
		t.Fatal("Table returned no predicate for a tenant-scoped model")
	}
	stmt, args, err := sqlbuilder.Build(pred)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stmt != "`orders`.`tenant_id` = ?" || len(args) != 1 || args[0] != "t-3" {
		t.Fatalf("predicate = %q args = %#v", stmt, args)
	}
}

// TestSelectDisabled covers the deployment switch: a resolver built with
// Disabled behaves like a connection without the plugin — no condition, and no
// error when the context carries no tenant. This is what keeps both isolation
// paths on one switch.
// TestSelectDisabled 覆盖部署开关：以 Disabled 构造的 Resolver 与未装插件的连接行为一致
// ——不加条件，ctx 无租户也不报错。两条隔离路径共用同一个开关靠的就是它。
func TestSelectDisabled(t *testing.T) {
	r := sqlmodel.New(sqlmodel.Options{Disabled: true})

	sel, err := r.Select(context.Background(), order{}, "id")
	if err != nil {
		t.Fatalf("Select with Disabled: %v", err)
	}
	stmt, args := build(t, sel)
	const want = "SELECT `orders`.`id` FROM `orders`"
	if stmt != want {
		t.Fatalf("stmt = %q, want %q", stmt, want)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v, want none", args)
	}

	// A tenant in ctx changes nothing either: the switch is not a per-request
	// decision.
	ctx := tenant.WithTenant(context.Background(), "t-1")
	sel, err = r.Select(ctx, order{}, "id")
	if err != nil {
		t.Fatalf("Select with Disabled and a tenant: %v", err)
	}
	if stmt, args := build(t, sel); stmt != want || len(args) != 0 {
		t.Fatalf("stmt = %q args = %#v, want the unfiltered statement", stmt, args)
	}
}

// TestModelRequired: nil, a typed nil pointer and an empty table name all fail
// with the same error instead of panicking or building `FROM`.
// TestModelRequired：nil、带类型的 nil 指针、空表名都返回同一个错误，而不是 panic 或
// 构造出没有表名的语句。
func TestModelRequired(t *testing.T) {
	var missing *order
	cases := []struct {
		name  string
		model sqlmodel.Model
	}{
		{name: "nil interface", model: nil},
		{name: "typed nil pointer", model: missing},
		{name: "empty table name", model: emptyName{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := sqlmodel.Select(context.Background(), tc.model, "id"); !errors.Is(err, sqlmodel.ErrModelRequired) {
				t.Fatalf("err = %v, want ErrModelRequired", err)
			}
		})
	}
}

type emptyName struct{}

func (emptyName) TableName() string { return "  " }
