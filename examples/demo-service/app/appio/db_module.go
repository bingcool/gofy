// Package appio 的 db-only 装配：worker 进程只需要数据库时，用它代替完整模块。
package appio

import (
	"context"
	"errors"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
	appdb "github.com/yourorg/demo-service/app/appio/db"
)

// dbModule assembles one generation that carries the database and nothing else.
// It exists for worker processes: they reuse the service and repository layer,
// which resolves handles through appio.WithContext(ctx).DBIO, but they do not
// need the broker connections the full Module opens — a consumer builds its own.
//
// 只装配数据库的模块。它服务 worker 进程：worker 复用 service 与 repository，
// 那些层通过 appio.WithContext(ctx).DBIO 取句柄；但 worker 不需要完整 Module 打开的
// broker 连接——消费者自己会建。
type dbModule struct {
	rt   *gofy.Runtime
	cfg  map[string]gofy.DatabaseConfig
	opts []appdb.OpenOption
	set  *appdb.Set
	snap *Snapshot
}

// NewDBOnlyModuleFactory builds a factory for a generation that carries only the
// database. Register it with gofy.WithWorkerModules, so the framework starts it
// before the first group and stops it after every group drained.
//
// The default pool is the framework's (Runtime.DB) and is only published, not
// owned: Stop closes the extra instances this generation opened and never the
// pool the App will close itself. A deployment without a `db` section still
// assembles: the generation has no default handle and DBIO reports
// ErrNotConfigured at the point of use, which is the same answer a request gets.
//
// Named instances (db.order, db.product) are opened here exactly as they are in
// the HTTP path, so a worker that reuses a service depending on one gets it
// without a second wiring path.
// NewDBOnlyModuleFactory 构造"只带数据库"的工厂。用 gofy.WithWorkerModules 注册，
// 框架因此在第一个分组之前启动它、在所有分组 drain 之后停止它。
//
// default 池归框架（Runtime.DB），这里只发布、不拥有：Stop 关闭本代打开的额外实例，
// 从不关闭 App 自己要关的池。没有 `db` 段的部署同样能装配：这一代没有 default 句柄，
// DBIO 在使用处返回 ErrNotConfigured——与请求得到的答案是同一个。
//
// 命名实例（db.order、db.product）在这里与 HTTP 路径一样打开，因此复用了依赖某个实例的
// service 的 worker 不需要第二条接线。
func NewDBOnlyModuleFactory(cfg map[string]gofy.DatabaseConfig, opts ...appdb.OpenOption) gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		return &dbModule{rt: rt, cfg: cfg, opts: opts}, nil
	}
}

// Name identifies the module in startup errors and shutdown logs.
// Name 在启动错误与停机日志里标识本模块。
func (m *dbModule) Name() string { return "io-db" }

// Register is a no-op: a worker process has no Router, and the framework never
// calls this on the worker path.
// Register 是空实现：worker 进程没有 Router，框架在 worker 路径上也不会调用它。
func (m *dbModule) Register(*support.Router) error { return nil }

// Start opens this generation's database handles and publishes the snapshot.
// Nothing is published until every instance is ready, so a failed start leaves
// no half generation for the first tick to resolve.
// Start 打开本代的数据库句柄并发布快照。全部实例就绪之前不发布任何东西，因此启动失败
// 不会给首个 tick 留下半代句柄。
func (m *dbModule) Start(ctx context.Context) error {
	if m.rt == nil {
		return errors.New("io-db: runtime is required")
	}
	set, err := appdb.Open(ctx, m.rt.DB, m.cfg, m.rt, m.opts...)
	if err != nil {
		return err
	}
	m.set = set
	m.snap = newSnapshot(set, nil, nil, nil, nil, nil, nil)
	Publish(m.snap)
	return nil
}

// Snapshot returns the generation this module assembled; nil before Start and
// after Stop. Diagnostics and tests read it instead of the process-wide Current.
// Snapshot 返回本模块组装的那一代；Start 之前与 Stop 之后为 nil。诊断与测试读它，
// 而不是进程级 Current。
func (m *dbModule) Snapshot() *Snapshot { return m.snap }

// Stop withdraws the snapshot this module published and closes the instances it
// opened. The framework's default pool is left alone: the App closes it after
// every module stopped.
// Stop 撤回本模块发布的快照并关闭它打开的实例。框架的 default 池不动：App 会在所有
// 模块停止之后关闭它。
func (m *dbModule) Stop(context.Context) error {
	currentSnapshot.CompareAndSwap(m.snap, nil)
	m.snap = nil
	return m.set.Close()
}
