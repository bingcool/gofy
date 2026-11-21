package sqlmodel_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/bingcool/gofy/support/frameworkcontext"
	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/bingcool/gofy/support/sqlbuilder"
	"github.com/bingcool/gofy/support/sqlmodel"
	"github.com/bingcool/gofy/support/tenant"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// counter keeps the in-memory DSN unique across -count reruns in one process.
// counter 保证 -count 重跑时内存库 DSN 不重复。
var counter atomic.Int64

// TestAllTenantPathsResolveTheSameIdentity pins the P2-02 fix: an explicit
// tenant override must win over the JWT identity on all three paths. Before the
// markers moved into frameworkcontext, the GORM plugin and sqlmodel used B while
// sqlbuilder.TenantScope kept using A — the same request filtered by two tenants
// depending on which query helper it happened to call.
// TestAllTenantPathsResolveTheSameIdentity 固定 P2-02 的修复：显式租户覆盖必须在三条路径上都
// 压过 JWT 身份。标记搬进 frameworkcontext 之前，插件与 sqlmodel 用 B、sqlbuilder.TenantScope
// 仍用 A——同一请求按调用到的查询辅助函数不同，会按两个租户过滤。
func TestAllTenantPathsResolveTheSameIdentity(t *testing.T) {
	base := frameworkcontext.WithUser(context.Background(), frameworkcontext.User{
		ID: "u-1", TenantID: "a",
	})
	ctx := tenant.WithTenant(base, "b")

	// 1. The shared marker itself.
	if got, ok := frameworkcontext.TenantOverride(ctx); !ok || got != "b" {
		t.Fatalf("frameworkcontext.TenantOverride = %q, %v", got, ok)
	}
	if got, ok := tenant.OverrideTenant(ctx); !ok || got != "b" {
		t.Fatalf("tenant.OverrideTenant = %q, %v", got, ok)
	}

	// 2. The builder helper.
	table := sqlbuilder.Table("orders")
	scope, ok := sqlbuilder.TenantScope(ctx, table)
	if !ok {
		t.Fatal("TenantScope returned no predicate")
	}
	_, args, err := sqlbuilder.Build(scope)
	if err != nil {
		t.Fatalf("Build scope: %v", err)
	}
	if len(args) != 1 || args[0] != "b" {
		t.Fatalf("TenantScope args = %v, want [b]", args)
	}

	// 3. The model-aware resolver.
	r := newResolver(t, sqlmodel.Options{})
	_, modelScope, err := r.Table(ctx, order{})
	if err != nil {
		t.Fatalf("Resolver.Table: %v", err)
	}
	if modelScope == nil {
		t.Fatal("Resolver.Table returned no tenant predicate")
	}
	_, args, err = sqlbuilder.Build(modelScope)
	if err != nil {
		t.Fatalf("Build model scope: %v", err)
	}
	if len(args) != 1 || args[0] != "b" {
		t.Fatalf("Resolver.Table args = %v, want [b]", args)
	}

	// 4. The GORM plugin, through a DryRun statement.
	db := openInstrumentedDB(t)
	stmt := db.WithContext(ctx).Session(&gorm.Session{DryRun: true}).Find(&[]order{}).Statement
	found := false
	for _, v := range stmt.Vars {
		if v == "b" {
			found = true
		}
		if v == "a" {
			t.Fatalf("plugin bound the JWT tenant a despite the override: vars = %v", stmt.Vars)
		}
	}
	if !found {
		t.Fatalf("plugin bound no overridden tenant: vars = %v; sql = %s", stmt.Vars, stmt.SQL.String())
	}
}

// TestTenantScopeBypassAndNil covers the two guards added with the identity fix:
// a bypassed ctx and a nil table both produce "no predicate" instead of a filter
// built from stale identity data or a nil-pointer panic.
// TestTenantScopeBypassAndNil 覆盖随身份修复加上的两道防护：被绕过的 ctx 与 nil 表都返回
// 「没有谓词」，而不是拿旧身份拼条件或直接 panic。
func TestTenantScopeBypassAndNil(t *testing.T) {
	base := frameworkcontext.WithUser(context.Background(), frameworkcontext.User{
		ID: "u-1", TenantID: "a",
	})
	if _, ok := sqlbuilder.TenantScope(tenant.Without(base), sqlbuilder.Table("orders")); ok {
		t.Fatal("TenantScope still scoped a bypassed context")
	}
	if _, ok := sqlbuilder.TenantScope(base, nil); ok {
		t.Fatal("TenantScope returned a predicate for a nil table")
	}
}

// openInstrumentedDB opens a unique in-memory SQLite database with the tenant
// plugin installed, so the plugin path can run without a server.
// openInstrumentedDB 打开一个唯一的进程内 SQLite 库并装好租户插件，插件路径因此无需真实
// 数据库。
func openInstrumentedDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:identity_%d?mode=memory&cache=shared", counter.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	// Migrate before Instrument, so the plugin never rewrites DDL.
	// 先迁移后装插件，插件因此不会改写 DDL。
	if err := db.AutoMigrate(&order{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if err := tenant.Instrument(db, tenant.Options{}); err != nil {
		t.Fatalf("Instrument: %v", err)
	}
	return db
}

// tenantVerdict is what one isolation path decided for a context: the tenant it
// would bind, or none.
// tenantVerdict 是一条隔离路径对某个 ctx 的判定：将要绑定的租户，或没有租户。
type tenantVerdict struct {
	ID string
	OK bool
}

func (v tenantVerdict) String() string {
	if !v.OK {
		return "none"
	}
	return fmt.Sprintf("%q", v.ID)
}

// pluginVerdict runs the plugin path as a DryRun query and reads the bound
// tenant back from the statement vars. A fail-closed error is the "no tenant"
// verdict, not a test failure.
// pluginVerdict 以 DryRun 查询走插件路径，从语句参数读回绑定的租户。失败关闭的错误算
// 「无租户」判定，不算测试失败。
func pluginVerdict(t *testing.T, db *gorm.DB, ctx context.Context) tenantVerdict {
	t.Helper()
	tx := db.WithContext(ctx).Session(&gorm.Session{DryRun: true}).Find(&[]order{})
	if tx.Error != nil {
		if errors.Is(tx.Error, tenant.ErrTenantRequired) {
			return tenantVerdict{}
		}
		t.Fatalf("plugin: %v", tx.Error)
	}
	vars := tx.Statement.Vars
	if len(vars) != 1 {
		t.Fatalf("plugin vars = %v, want exactly the tenant", vars)
	}
	return tenantVerdict{ID: fmt.Sprint(vars[0]), OK: true}
}

// modelVerdict reads the predicate sqlmodel attaches for the same model.
// modelVerdict 读取 sqlmodel 为同一模型挂上的谓词。
func modelVerdict(t *testing.T, r *sqlmodel.Resolver, ctx context.Context) tenantVerdict {
	t.Helper()
	_, pred, err := r.Table(ctx, order{})
	if err != nil {
		if errors.Is(err, tenant.ErrTenantRequired) {
			return tenantVerdict{}
		}
		t.Fatalf("sqlmodel: %v", err)
	}
	if pred == nil {
		return tenantVerdict{}
	}
	_, args, err := sqlbuilder.Build(pred)
	if err != nil {
		t.Fatalf("build model predicate: %v", err)
	}
	if len(args) != 1 {
		t.Fatalf("model args = %v, want exactly the tenant", args)
	}
	return tenantVerdict{ID: fmt.Sprint(args[0]), OK: true}
}

// scopeVerdict reads the predicate the gorm-free builder path emits.
// scopeVerdict 读取不依赖 gorm 的构造器路径给出的谓词。
func scopeVerdict(t *testing.T, ctx context.Context) tenantVerdict {
	t.Helper()
	pred, ok := sqlbuilder.TenantScope(ctx, sqlbuilder.Table("orders"))
	if !ok {
		return tenantVerdict{}
	}
	_, args, err := sqlbuilder.Build(pred)
	if err != nil {
		t.Fatalf("build scope predicate: %v", err)
	}
	if len(args) != 1 {
		t.Fatalf("scope args = %v, want exactly the tenant", args)
	}
	return tenantVerdict{ID: fmt.Sprint(args[0]), OK: true}
}

// TestTenantPathAgreement pins the P2-01/P2-02 fix end to end: for one ctx all
// three isolation paths — GORM plugin, sqlmodel and sqlbuilder.TenantScope —
// must bind the same tenant, and a blank override must fall back to the JWT
// identity instead of masking it or emitting a whitespace predicate.
// TestTenantPathAgreement 端到端固定 P2-01/P2-02 的修复：同一个 ctx 在三条隔离路径——
// GORM 插件、sqlmodel 与 sqlbuilder.TenantScope——上必须绑定同一个租户；纯空白 override
// 必须回退到 JWT 身份，而不是盖住身份或拼出空白谓词。
func TestTenantPathAgreement(t *testing.T) {
	identity := frameworkcontext.WithUser(context.Background(), frameworkcontext.User{
		ID: "u-1", TenantID: "a",
	})
	// Programmatic propagation skips the trimming CaptureIncoming does, so this
	// ctx carries the padding into every read path.
	// 程序化透传跳过了 CaptureIncoming 的去空白，因此这个 ctx 会把空白带进每条读取路径。
	propagated := headerpropagation.ContextWithHeader(
		context.Background(), headerpropagation.HeaderTenantID, " a ")
	cases := []struct {
		name string
		ctx  context.Context
		want tenantVerdict
	}{
		{name: "identity only", ctx: identity, want: tenantVerdict{ID: "a", OK: true}},
		{name: "empty override falls back", ctx: tenant.WithTenant(identity, ""), want: tenantVerdict{ID: "a", OK: true}},
		{name: "blank override falls back", ctx: tenant.WithTenant(identity, "   "), want: tenantVerdict{ID: "a", OK: true}},
		{name: "padded override is trimmed", ctx: tenant.WithTenant(identity, " b "), want: tenantVerdict{ID: "b", OK: true}},
		{name: "padded propagated header is trimmed", ctx: propagated, want: tenantVerdict{ID: "a", OK: true}},
		{name: "no identity", ctx: context.Background(), want: tenantVerdict{}},
		{name: "no identity and blank override", ctx: tenant.WithTenant(context.Background(), "  "), want: tenantVerdict{}},
	}
	r := newResolver(t, sqlmodel.Options{})
	db := openInstrumentedDB(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := []struct {
				name string
				got  tenantVerdict
			}{
				{"plugin", pluginVerdict(t, db, tc.ctx)},
				{"sqlmodel", modelVerdict(t, r, tc.ctx)},
				{"TenantScope", scopeVerdict(t, tc.ctx)},
			}
			for _, path := range paths {
				if path.got != tc.want {
					t.Fatalf("%s = %s, want %s", path.name, path.got, tc.want)
				}
			}
		})
	}
}
