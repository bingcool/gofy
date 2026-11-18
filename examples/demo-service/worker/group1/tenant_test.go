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

	appdb "github.com/yourorg/demo-service/app/appio/db"
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

// installTenantAtAssembly stands in for the process assembly, which is the only
// place that sees every pool of a generation. Worker entries no longer install
// the plugin themselves: they receive a pool that is already filtered.
// installTenantAtAssembly 代替进程装配，那是唯一能看到一代里全部池的位置。worker
// 入口不再自己安装插件：它们拿到的池已经带上过滤。
func installTenantAtAssembly(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := appdb.EnsureTenant(db, tenant.Options{}); err != nil {
		t.Fatalf("install tenant plugin: %v", err)
	}
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

// The delivery path: the plugin was installed at assembly, and the event's
// tenant decides which rows the handler can see. A second delivery must not
// fail — the same db.default is reused by every message in the process.
// 投递路径：插件在装配时已安装，事件租户决定 handler 能看到哪些行。第二次投递不能
// 失败——同一进程里每条消息复用的都是同一个 db.default。
func TestWithOrderTenantAppliesEventTenant(t *testing.T) {
	db := newTenantProbeDB(t)
	installTenantAtAssembly(t, db)
	seedTenantProbe(t, db)

	ctx := withOrderTenant(context.Background(), " a ")
	var got []tenantProbeRow
	if err := db.WithContext(ctx).Find(&got).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 || got[0].TenantID != "a" {
		t.Fatalf("event tenant was not applied: %+v", got)
	}

	// The second delivery names another tenant on the same pool; it must see
	// that tenant's row, not the first one's and not both.
	// 第二条投递在同一个池上指名另一个租户；它必须看到那个租户的行，既不是第一条的，
	// 也不是两条都看到。
	second := withOrderTenant(context.Background(), "b")
	got = nil
	if err := db.WithContext(second).Find(&got).Error; err != nil {
		t.Fatalf("second Find: %v", err)
	}
	if len(got) != 1 || got[0].TenantID != "b" {
		t.Fatalf("second delivery saw %+v, want tenant b only", got)
	}
}

// Disabled means untouched: no plugin is installed at assembly, so an event
// tenant changes nothing and the statement keeps ordinary GORM behaviour.
// 关闭即不触碰：装配时不装插件，事件租户不改变任何行为，语句保持普通 GORM 语义。
func TestWithOrderTenantDisabledLeavesStatementsUntouched(t *testing.T) {
	db := newTenantProbeDB(t)
	seedTenantProbe(t, db)

	ctx := withOrderTenant(context.Background(), "a")
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
	installTenantAtAssembly(t, db)
	seedTenantProbe(t, db)

	ctx := withOrderTenant(context.Background(), "  ")
	var got []tenantProbeRow
	err := db.WithContext(ctx).Find(&got).Error
	if !errors.Is(err, tenant.ErrTenantRequired) {
		t.Fatalf("statement without a tenant = %v, want ErrTenantRequired", err)
	}
}

// A schema-less statement — Table plus a scalar dest — gives the plugin no
// model to probe for the tenant column, so it fails closed instead of guessing.
// Cross-tenant aggregates must therefore say so with Without; the scan and
// report loops rely on this behaviour being explicit rather than accidental.
// 无 schema 的语句——Table 加标量目标——没有可供插件探测租户列的模型，因此它失败
// 关闭而不是猜测。跨租户聚合必须用 Without 显式声明；扫描与报表循环依赖的正是
// 这一行为是显式的，而不是碰巧的。
func TestSchemaLessAggregateFailsClosed(t *testing.T) {
	db := newTenantProbeDB(t)
	installTenantAtAssembly(t, db)

	var n int64
	err := db.WithContext(context.Background()).Table("tenant_probe_rows").Count(&n).Error
	if !errors.Is(err, tenant.ErrTenantRequired) {
		t.Fatalf("schema-less aggregate = %v, want ErrTenantRequired", err)
	}
}

// The claim producer writes task rows with Table().Create(map): no model, so the
// plugin has nothing to probe and fails closed unless the statement says it is
// cross-tenant. Enabling isolation used to break the whole sample queue here.
// 认领生产者用 Table().Create(map) 写任务行：没有 model，插件无从探测，除非语句显式
// 声明自己是跨租户的，否则失败关闭。开启隔离曾让整条示例队列卡死在这里。
func TestClaimEnqueueIsCrossTenant(t *testing.T) {
	db := newTenantProbeDB(t)
	installTenantAtAssembly(t, db)
	if err := db.Exec(`CREATE TABLE claim_jobs (
		job_type TEXT, status INTEGER, payload TEXT, next_run_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create claim table: %v", err)
	}

	cfg := ClaimScanConfig{Table: "claim_jobs", Job: "order-settle"}
	if err := enqueueClaimJob(context.Background(), db, cfg, claimJob{userID: 1, note: "x"}); err != nil {
		t.Fatalf("enqueue with tenant isolation on: %v", err)
	}

	var n int64
	countCtx := tenant.Without(context.Background())
	if err := db.WithContext(countCtx).Table("claim_jobs").Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("enqueued rows = %d, want 1", n)
	}
}
