package appio

import (
	"context"
	"errors"
	"testing"

	"github.com/bingcool/gofy/core"
	gofydb "github.com/bingcool/gofy/support/db"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofysqlite "github.com/bingcool/gofy/support/sqlite"
	"github.com/bingcool/gofy/support/tenant"
	appdb "github.com/yourorg/demo-service/app/appio/db"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// newDBOnlyRuntime opens an in-memory pool and wraps it the way the framework
// hands it to a worker module: as Runtime.DB, not as an appio generation.
// newDBOnlyRuntime 打开一个内存池，并按框架交给 worker 模块的方式包装它：作为
// Runtime.DB，而不是一代 appio。
func newDBOnlyRuntime(t *testing.T) (*core.Runtime, *gorm.DB) {
	t.Helper()
	db, err := gofysqlite.New(context.Background(), gofysqlite.Config{
		DSN:      "file:db_only_module?mode=memory&cache=shared",
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
	// The published generation is process-wide; withdraw it so a later test in
	// this package starts from the state it expects.
	// 已发布的代际是进程级的；撤回它，让本包后续用例从它预期的状态开始。
	t.Cleanup(func() { Publish(nil) })
	return core.NewRuntime(core.RuntimeConfig{Logger: zap.NewNop(), DB: db}), db
}

// TestDBOnlyModulePublishesTheDefaultHandle is the whole contract a worker
// needs: after Start, business code takes a handle the way a request does and
// gets the pool the framework opened.
// TestDBOnlyModulePublishesTheDefaultHandle 就是 worker 需要的全部契约：Start 之后，
// 业务代码按请求的方式取句柄，拿到的正是框架打开的那个池。
func TestDBOnlyModulePublishesTheDefaultHandle(t *testing.T) {
	rt, pool := newDBOnlyRuntime(t)
	mod, err := NewDBOnlyModuleFactory(nil)(rt)
	if err != nil {
		t.Fatalf("build module: %v", err)
	}
	if err := mod.(*dbModule).Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = mod.(*dbModule).Stop(context.Background()) })

	got, err := WithContext(context.Background()).NewDBIO(context.Background(), appdb.DefaultInstance)
	if err != nil {
		t.Fatalf("NewDBIO: %v", err)
	}
	if got != pool {
		t.Fatalf("NewDBIO returned a different pool than Runtime.DB")
	}
}

// TestDBOnlyModuleLeavesOtherSectionsUnconfigured pins that a missing section is
// an error at the point of use, not a nil handle or a panic: a worker that only
// needs the database must not be forced to open redis to satisfy the snapshot.
// TestDBOnlyModuleLeavesOtherSectionsUnconfigured 固定"未装配的段在使用处报错，
// 而不是 nil 句柄或 panic"：只需要数据库的 worker 不该为了满足快照去开 redis。
func TestDBOnlyModuleLeavesOtherSectionsUnconfigured(t *testing.T) {
	rt, _ := newDBOnlyRuntime(t)
	mod, err := NewDBOnlyModuleFactory(nil)(rt)
	if err != nil {
		t.Fatalf("build module: %v", err)
	}
	if err := mod.(*dbModule).Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = mod.(*dbModule).Stop(context.Background()) })

	snap := WithContext(context.Background())
	if _, err := snap.RedisIO(context.Background(), "cache"); !errors.Is(err, gofyredis.ErrNotConfigured) {
		t.Fatalf("RedisIO error = %v, want ErrNotConfigured", err)
	}
	if _, err := snap.NewDBIO(context.Background(), "order"); !errors.Is(err, gofydb.ErrUnknownInstance) {
		t.Fatalf("NewDBIO(order) error = %v, want ErrUnknownInstance", err)
	}
}

// TestDBOnlyModuleStopWithdrawsTheGeneration keeps the shutdown contract: once
// Stop returns, the process must not resolve a pool the App is about to close.
// TestDBOnlyModuleStopWithdrawsTheGeneration 固定停机契约：Stop 返回之后，进程
// 不得再解析到 App 即将关闭的池。
func TestDBOnlyModuleStopWithdrawsTheGeneration(t *testing.T) {
	rt, _ := newDBOnlyRuntime(t)
	mod, err := NewDBOnlyModuleFactory(nil)(rt)
	if err != nil {
		t.Fatalf("build module: %v", err)
	}
	if err := mod.(*dbModule).Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := mod.(*dbModule).Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if Current() != nil {
		t.Fatalf("snapshot still published after Stop")
	}
	if mod.(*dbModule).Snapshot() != nil {
		t.Fatalf("module still reports a snapshot after Stop")
	}
}

// TestDBOnlyModuleWithoutDefaultStillAssembles keeps the "db is an optional
// section" rule: assembly succeeds and the failure surfaces as
// ErrNotConfigured at the first use, naming the missing instance.
// TestDBOnlyModuleWithoutDefaultStillAssembles 保持"db 是可选段"的规则：装配成功，
// 失败在首次取用时以 ErrNotConfigured 出现，并指明缺的是哪个实例。
func TestDBOnlyModuleWithoutDefaultStillAssembles(t *testing.T) {
	rt := core.NewRuntime(core.RuntimeConfig{Logger: zap.NewNop()})
	mod, err := NewDBOnlyModuleFactory(nil)(rt)
	if err != nil {
		t.Fatalf("build module: %v", err)
	}
	if err := mod.(*dbModule).Start(context.Background()); err != nil {
		t.Fatalf("start without a default pool: %v", err)
	}
	t.Cleanup(func() { _ = mod.(*dbModule).Stop(context.Background()) })

	if _, err := WithContext(context.Background()).NewDBIO(context.Background(), appdb.DefaultInstance); !errors.Is(err, gofydb.ErrNotConfigured) {
		t.Fatalf("NewDBIO error = %v, want ErrNotConfigured", err)
	}
}

// TestDBOnlyModuleInstallsTenantPluginAtAssembly is the reason workers no longer
// install the plugin themselves: the option travels with the assembly, so every
// pool of the generation is filtered before the first worker runs. A schema-less
// statement is the cheapest proof — a filtered pool fails closed on it.
// TestDBOnlyModuleInstallsTenantPluginAtAssembly 就是 worker 不再自己装插件的原因：
// 选项随装配走，这一代的每个池在第一个 worker 运行之前就带上过滤。无 schema 的语句
// 是最省事的证明——带过滤的池会在它上面失败关闭。
func TestDBOnlyModuleInstallsTenantPluginAtAssembly(t *testing.T) {
	rt, pool := newDBOnlyRuntime(t)
	mod, err := NewDBOnlyModuleFactory(nil, appdb.WithTenant(tenant.Options{}))(rt)
	if err != nil {
		t.Fatalf("build module: %v", err)
	}
	if err := mod.(*dbModule).Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = mod.(*dbModule).Stop(context.Background()) })

	var n int64
	db, err := WithContext(context.Background()).NewDBIO(context.Background(), appdb.DefaultInstance)
	if err != nil {
		t.Fatalf("NewDBIO: %v", err)
	}
	if got := db.WithContext(context.Background()).Table("orders").Count(&n).Error; !errors.Is(got, tenant.ErrTenantRequired) {
		t.Fatalf("schema-less aggregate on an assembled pool = %v, want ErrTenantRequired", got)
	}
	if db != pool {
		t.Fatalf("assembly did not publish the framework pool")
	}
}
