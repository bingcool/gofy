package db

import (
	"context"
	"testing"

	"github.com/bingcool/gofy/support/tenant"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type tenantRow struct {
	ID       uint   `gorm:"primaryKey;autoIncrement"`
	TenantID string `gorm:"column:tenant_id"`
	Code     string
}

func (tenantRow) TableName() string { return "tenant_rows" }

func testPool(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&tenantRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestInstallTenantAppliesFilter(t *testing.T) {
	pool := testPool(t)
	if err := installTenant(pool, tenant.Options{}); err != nil {
		t.Fatalf("installTenant: %v", err)
	}

	bypass := tenant.Without(context.Background())
	if err := pool.WithContext(bypass).Create(&tenantRow{TenantID: "a", Code: "x"}).Error; err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := pool.WithContext(bypass).Create(&tenantRow{TenantID: "b", Code: "y"}).Error; err != nil {
		t.Fatalf("seed b: %v", err)
	}

	// An explicit tenant in ctx limits the query to that tenant's rows.
	ctx := tenant.WithTenant(context.Background(), "a")
	var got []tenantRow
	if err := pool.WithContext(ctx).Find(&got).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 || got[0].TenantID != "a" {
		t.Fatalf("tenant filter missing at the demo assembly layer: %+v", got)
	}
}

func TestInstallTenantIsIdempotentAcrossGenerations(t *testing.T) {
	// db.default is owned by the framework and reused across Reload
	// generations, so Open calls installTenant on the same *gorm.DB again.
	// A second db.Use would fail with a duplicate-callback error.
	// db.default 由框架持有、跨 Reload 代复用，Open 会对同一个 *gorm.DB 再次调用
	// installTenant；重复 db.Use 会因回调重复注册而失败。
	pool := testPool(t)
	for i := 0; i < 3; i++ {
		if err := installTenant(pool, tenant.Options{}); err != nil {
			t.Fatalf("installTenant call %d: %v", i+1, err)
		}
	}
	ctx := tenant.WithTenant(context.Background(), "a")
	var got []tenantRow
	if err := pool.WithContext(ctx).Find(&got).Error; err != nil {
		t.Fatalf("Find after repeated installs: %v", err)
	}
}

// EnsureTenant is the entry worker groups use: they never open a generation,
// so they install the plugin on the framework's db.default instead. The
// exported form must keep both properties installTenant has — the filter takes
// effect and a repeated call is a no-op rather than a duplicate-callback error.
// EnsureTenant 是 worker 分组使用的入口：它们不打开装配代，因此在框架的 db.default
// 上安装插件。导出形式必须保留 installTenant 的两个性质——过滤生效、重复调用是
// 空操作而不是回调重复注册错误。
func TestEnsureTenantIsIdempotentAndAppliesFilter(t *testing.T) {
	pool := testPool(t)
	opts := tenant.Options{}
	for i := 0; i < 2; i++ {
		if err := EnsureTenant(pool, opts); err != nil {
			t.Fatalf("EnsureTenant call %d: %v", i+1, err)
		}
	}

	if err := pool.WithContext(tenant.Without(context.Background())).
		Create(&tenantRow{TenantID: "a", Code: "x"}).Error; err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := pool.WithContext(tenant.Without(context.Background())).
		Create(&tenantRow{TenantID: "b", Code: "y"}).Error; err != nil {
		t.Fatalf("seed b: %v", err)
	}

	var got []tenantRow
	if err := pool.WithContext(tenant.WithTenant(context.Background(), "a")).
		Find(&got).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 || got[0].TenantID != "a" {
		t.Fatalf("EnsureTenant did not apply the filter: %+v", got)
	}
}
