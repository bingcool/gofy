package group1

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.uber.org/zap"
	"gorm.io/gorm"

	gofysqlite "github.com/bingcool/gofy/support/sqlite"
	"github.com/bingcool/gofy/support/tenant"
)

// tenantProbeRow stands in for a tenant-aware business table. The demo's orders
// table declares no tenant column on purpose (its migration predates the
// plugin), so the filter has to be proven on a model that does.
// tenantProbeRow 代表租户感知的业务表。示例的 orders 表有意不声明租户列（它的迁移
// 早于插件），因此过滤要在真正带该列的模型上验证。
type tenantProbeRow struct {
	ID       uint   `gorm:"primaryKey;autoIncrement"`
	TenantID string `gorm:"column:tenant_id"`
	Code     string
}

func (tenantProbeRow) TableName() string { return "tenant_probe_rows" }

// newTenantProbeDB opens one in-memory SQLite per test. The DSN is derived from
// the test name because the plugin is installed on the *gorm.DB, not on the
// context: a shared database would carry one case's plugin into the next.
// newTenantProbeDB 为每个用例打开一个独立内存 SQLite。DSN 由用例名派生，因为插件
// 装在 *gorm.DB 上而不是 context 上：共用数据库会把前一个用例的插件带进下一个。
func newTenantProbeDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:tenant_probe_%s?mode=memory&cache=shared",
		strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	db, err := gofysqlite.New(context.Background(), gofysqlite.Config{
		DSN:      dsn,
		LogLevel: "silent",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if err := gofysqlite.Close(db); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	if err := db.AutoMigrate(&tenantProbeRow{}); err != nil {
		t.Fatalf("migrate probe rows: %v", err)
	}
	return db
}

// seedTenantProbe writes one row per tenant, bypassing the plugin: the rows are
// fixture data, not a statement that belongs to any tenant.
// seedTenantProbe 为每个租户写一行，并绕过插件：这些是夹具数据，不是属于某个租户的语句。
func seedTenantProbe(t *testing.T, db *gorm.DB) {
	t.Helper()
	bypass := tenant.Without(context.Background())
	for _, row := range []tenantProbeRow{
		{TenantID: "a", Code: "x"},
		{TenantID: "b", Code: "y"},
	} {
		if err := db.WithContext(bypass).Create(&row).Error; err != nil {
			t.Fatalf("seed tenant %s: %v", row.TenantID, err)
		}
	}
}

// The delivery path: the plugin is installed on the first statement and the
// event's tenant decides which rows the handler can see. A second delivery must
// not fail — the same db.default is reused by every message in the process.
// 投递路径：插件在第一条语句前安装，事件租户决定 handler 能看到哪些行。第二次投递
// 不能失败——同一进程里每条消息复用的都是同一个 db.default。
func TestWithOrderTenantAppliesEventTenant(t *testing.T) {
	db := newTenantProbeDB(t)
	opts := tenant.Options{}
	orderTenant.Store(&opts)
	t.Cleanup(func() { orderTenant.Store(nil) })
	seedTenantProbe(t, db)

	ctx, err := withOrderTenant(context.Background(), db, " a ")
	if err != nil {
		t.Fatalf("withOrderTenant: %v", err)
	}
	var got []tenantProbeRow
	if err := db.WithContext(ctx).Find(&got).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 || got[0].TenantID != "a" {
		t.Fatalf("event tenant was not applied: %+v", got)
	}

	// The second message re-installs on the same pool; a duplicate callback
	// would surface here, not at startup.
	// 第二条消息会在同一个池上再次安装；回调重复注册会在这里暴露，而不是在启动时。
	if _, err := withOrderTenant(context.Background(), db, "b"); err != nil {
		t.Fatalf("second withOrderTenant: %v", err)
	}
}

// Disabled means untouched: no plugin, so an event tenant changes nothing and
// the statement keeps ordinary GORM behaviour.
// 关闭即不触碰：不装插件，事件租户不改变任何行为，语句保持普通 GORM 语义。
func TestWithOrderTenantDisabledLeavesStatementsUntouched(t *testing.T) {
	db := newTenantProbeDB(t)
	orderTenant.Store(nil)
	t.Cleanup(func() { orderTenant.Store(nil) })
	seedTenantProbe(t, db)

	ctx, err := withOrderTenant(context.Background(), db, "a")
	if err != nil {
		t.Fatalf("withOrderTenant: %v", err)
	}
	var got []tenantProbeRow
	if err := db.WithContext(ctx).Find(&got).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("disabled tenant changed the statement: got %d rows", len(got))
	}
}

// An event without a tenant is not guessed at: the plugin fails closed, so the
// handler retries instead of silently reading every tenant's rows.
// 事件不带租户时不做猜测：插件失败关闭，handler 因此重试，而不是静默读到全部租户的行。
func TestWithOrderTenantWithoutIDFailsClosed(t *testing.T) {
	db := newTenantProbeDB(t)
	opts := tenant.Options{}
	orderTenant.Store(&opts)
	t.Cleanup(func() { orderTenant.Store(nil) })
	seedTenantProbe(t, db)

	ctx, err := withOrderTenant(context.Background(), db, "  ")
	if err != nil {
		t.Fatalf("withOrderTenant: %v", err)
	}
	var got []tenantProbeRow
	err = db.WithContext(ctx).Find(&got).Error
	if !errors.Is(err, tenant.ErrTenantRequired) {
		t.Fatalf("statement without a tenant = %v, want ErrTenantRequired", err)
	}
}
