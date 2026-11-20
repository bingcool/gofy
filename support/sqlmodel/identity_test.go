package sqlmodel_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/bingcool/gofy/support/frameworkcontext"
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
	if err := db.AutoMigrate(&order{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if err := tenant.Instrument(db, tenant.Options{}); err != nil {
		t.Fatalf("Instrument: %v", err)
	}
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
