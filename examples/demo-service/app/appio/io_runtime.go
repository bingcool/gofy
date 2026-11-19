// Package appio wires the demo's named MySQL, Redis, AMQP, Kafka and RocketMQ
// instances into the handles published by their subpackages. It owns the extra
// connections: Start opens and installs them, Stop closes them, and a reload
// rebuilds them together with the module.
//
// The publish/pin mechanics come from support/generation; this package only
// assembles one generation of handles and declares the demo's entry points
// (the framework request binder, WithContext, and the module lifecycle).
//
// HTTP requests are pinned by the framework: the module implements
// support.RequestBinder, so the snapshot is bound to a request only after that
// request acquired the lease of the generation this module belongs to. Scripts
// and other non-HTTP entries call Snapshot() and pin it themselves. Reading the
// process-wide Current() is a fallback for code with no entry at all.
//
// 业务代码通过 Snapshot 取连接：本模块实现 support.RequestBinder，框架只在请求
// 拿到"本模块所属那一代"的 lease 之后才把快照固定进 ctx，Repository 与 Service
// 用 WithContext(ctx) 读取；脚本等非 HTTP 入口调用 Snapshot() 自行固定；完全没有
// 入口的代码才回退到 Current()。发布与固定机制由 support/generation 提供，本包
// 只负责组装一代句柄并声明 demo 的入口。
package appio

import (
	"context"
	"errors"
	"fmt"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
	gofylock "github.com/bingcool/gofy/support/lock"
	goredis "github.com/redis/go-redis/v9"
	appamqp "github.com/yourorg/demo-service/app/appio/amqp"
	appdb "github.com/yourorg/demo-service/app/appio/db"
	appkafka "github.com/yourorg/demo-service/app/appio/kafka"
	appredis "github.com/yourorg/demo-service/app/appio/redis"
	appredislock "github.com/yourorg/demo-service/app/appio/redislock"
	approcketmq "github.com/yourorg/demo-service/app/appio/rocketmq"
	appuuidint "github.com/yourorg/demo-service/app/appio/uuidint"
)

// Module publishes app/appio/db, app/appio/redis, app/appio/amqp, app/appio/kafka,
// app/appio/rocketmq, app/appio/uuidint and app/appio/redislock handles during
// startup.
// Module 在启动时打开各实例并发布 db、redis、amqp、kafka、rocketmq、uuidint、
// redislock 的包级句柄；它不注册路由，只负责连接生命周期与快照发布。
type Module struct {
	rt          *gofy.Runtime
	dbCfg       map[string]gofy.DatabaseConfig
	redisCfg    appredis.Config
	amqpCfg     appamqp.Config
	kafkaCfg    appkafka.Config
	rocketmqCfg approcketmq.Config
	uuidintCfg  appuuidint.Config
	lockCfg     gofylock.Config
	dbOpts      []appdb.OpenOption

	// closers records what Start opened, in opening order. Stop and the failure
	// path walk it backwards, so "close dependents before their dependencies" is
	// declared once instead of being repeated in every branch.
	// closers 按打开顺序记录 Start 打开的资源。Stop 与失败回滚都反向遍历它，
	// "先停依赖者、再关被依赖者"因此只声明一次，而不是在每个分支各写一遍。
	closers []namedCloser
	// snapshot 是本代句柄的只读视图，Start 发布、Stop 只撤回仍指向自己的快照。
	snapshot *Snapshot
}

// NewModuleFactory builds the module from the application runtime.
// NewModuleFactory 把实例配置封进模块工厂；每次装配由框架传入当代的 Runtime。
func NewModuleFactory(dbCfg map[string]gofy.DatabaseConfig, redisCfg appredis.Config, amqpCfg appamqp.Config, kafkaCfg appkafka.Config, rocketmqCfg approcketmq.Config, uuidintCfg appuuidint.Config, lockCfg gofylock.Config, dbOpts ...appdb.OpenOption) gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		return NewModule(rt, dbCfg, redisCfg, amqpCfg, kafkaCfg, rocketmqCfg, uuidintCfg, lockCfg, dbOpts...), nil
	}
}

// NewModule builds a module around rt and the instance configuration.
// NewModule 用本代 Runtime 与实例配置构造模块；构造函数不建连，连接要到 Start
// 才建立，因此装配失败不会留下半开的连接。
func NewModule(rt *gofy.Runtime, dbCfg map[string]gofy.DatabaseConfig, redisCfg appredis.Config, amqpCfg appamqp.Config, kafkaCfg appkafka.Config, rocketmqCfg approcketmq.Config, uuidintCfg appuuidint.Config, lockCfg gofylock.Config, dbOpts ...appdb.OpenOption) *Module {
	return &Module{
		rt:          rt,
		dbCfg:       dbCfg,
		redisCfg:    redisCfg,
		amqpCfg:     amqpCfg,
		kafkaCfg:    kafkaCfg,
		rocketmqCfg: rocketmqCfg,
		uuidintCfg:  uuidintCfg,
		lockCfg:     lockCfg,
		dbOpts:      dbOpts,
	}
}

// namedCloser is one resource and the name used when its Close fails, so a
// failure says which resource it came from instead of only carrying the driver
// error.
// namedCloser 是一个资源，以及其 Close 失败时用于报错的名字：失败因此能指出是
// 哪一个资源，而不只是驱动返回的错误。
type namedCloser struct {
	name  string
	close func() error
}

// closeAll closes the recorded resources in reverse opening order: nothing may
// outlive what it depends on. The UUID prefetch pool is the case that matters —
// its ticker talks to Redis, so stopping it after the clients were closed turns
// a reload into "redis: client is closed" retries and error callbacks.
//
// Every Close runs even when an earlier one fails, and the errors are joined
// with the resource name: a leaked connection is worse than a noisy error.
// closeAll 按打开顺序的逆序关闭资源：任何组件都不能比它依赖的东西活得更久。
// 真正要紧的是 UUID 预取池——它的 ticker 会访问 Redis，若在客户端关闭之后才停，
// 一次 Reload 就会变成 "redis: client is closed" 重试与错误回调。
//
// 即使前面的 Close 失败，后面的也都会执行；错误按资源名聚合：连接泄漏比错误噪音
// 更糟。
func closeAll(closers []namedCloser) error {
	errs := make([]error, 0, len(closers))
	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i].close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", closers[i].name, err))
		}
	}
	return errors.Join(errs...)
}

// Name returns the module name.
// Name 返回模块名 "io"，框架用它做模块去重与错误标识。
func (m *Module) Name() string { return "io" }

// Register is a no-op: this module only publishes connection handles.
// Register 是空实现：本模块只发布连接句柄，路由由业务模块注册。
func (m *Module) Register(*support.Router) error { return nil }

// Start opens the extra instances and publishes every handle.
// Start 依次打开各实例，任一失败都会按相反顺序关掉已建的连接再返回错误；全部
// 就绪后才安装句柄并一次发布快照，避免请求读到半成品。
func (m *Module) Start(ctx context.Context) error {
	if m.rt == nil {
		return errors.New("io: runtime is required")
	}
	// opened is the single record of what this generation owns, in opening
	// order. Every failure path and Stop close it backwards, so the dependency
	// order — UUID and the lock hold this generation's Redis clients — is
	// expressed once, next to the opens that create it.
	// opened 是本代拥有资源的唯一记录，按打开顺序排列。所有失败路径与 Stop 都
	// 反向关闭它，依赖关系（UUID 与锁持有本代的 Redis 客户端）因此只写在打开它们的
	// 位置旁边，不会在多处各写一份。
	opened := make([]namedCloser, 0, 7)

	dbSet, err := appdb.Open(ctx, m.rt.DB, m.dbCfg, m.rt, m.dbOpts...)
	if err != nil {
		return err
	}
	opened = append(opened, namedCloser{name: "db", close: dbSet.Close})

	redisSet, err := appredis.Open(ctx, m.redisCfg, m.rt)
	if err != nil {
		_ = closeAll(opened)
		return err
	}
	opened = append(opened, namedCloser{name: "redis", close: redisSet.Close})

	amqpSet, err := appamqp.Open(ctx, m.amqpCfg, m.rt.Logger)
	if err != nil {
		_ = closeAll(opened)
		return err
	}
	opened = append(opened, namedCloser{name: "amqp", close: amqpSet.Close})

	kafkaSet, err := appkafka.Open(ctx, m.kafkaCfg, m.rt.Logger)
	if err != nil {
		_ = closeAll(opened)
		return err
	}
	opened = append(opened, namedCloser{name: "kafka", close: kafkaSet.Close})

	rocketmqSet, err := approcketmq.Open(ctx, m.rocketmqCfg, m.rt.Logger)
	if err != nil {
		_ = closeAll(opened)
		return err
	}
	opened = append(opened, namedCloser{name: "rocketmq", close: rocketmqSet.Close})

	// The generator is pinned to this generation's Redis clients, so Get does
	// not depend on the last installed Registry. An empty fallback alias skips
	// the lookup; a configured one without an enabled instance fails in Open.
	// 生成器绑定本代 Redis 客户端，取号不再依赖最后安装的包级 Registry。备用别名
	// 为空时不查客户端；配置了却没有已启用的实例会由 Open 直接报错。
	fallbackName := m.uuidintCfg.FallbackRedisInstanceName()
	var fallbackClient *goredis.Client
	if fallbackName != "" {
		fallbackClient = redisSet.Client(fallbackName)
	}
	uuidintSet, err := appuuidint.Open(
		m.uuidintCfg,
		m.rt.Logger,
		redisSet.Client(m.uuidintCfg.RedisInstanceName()),
		fallbackClient,
	)
	if err != nil {
		_ = closeAll(opened)
		return err
	}
	// Recorded before StartPool: once the ticker runs it reads Redis, so even a
	// failed start has to stop the pool before the clients are closed.
	// 在 StartPool 之前记录：ticker 一旦运行就会读 Redis，因此即使启动失败，
	// 也必须在关闭客户端之前先停掉池。
	opened = append(opened, namedCloser{name: "uuidint", close: uuidintSet.Close})
	if err := uuidintSet.Start(ctx); err != nil {
		_ = closeAll(opened)
		return err
	}

	// The lock is pinned to this generation's Redis client for the same reason
	// the generator is: a reload must not move an acquired lock's connection.
	// 锁与生成器同因：绑定本代 Redis 客户端，Reload 不会改变已持有锁的连接。
	lockSet, err := appredislock.Open(m.lockCfg, m.rt.Logger, redisSet.Client(m.lockCfg.RedisInstanceName()))
	if err != nil {
		_ = closeAll(opened)
		return err
	}
	opened = append(opened, namedCloser{name: "lock", close: lockSet.Close})

	m.closers = opened
	dbSet.Install()
	redisSet.Install()
	amqpSet.Install()
	kafkaSet.Install()
	rocketmqSet.Install()
	// uuidint 与 redislock 依赖 redis 的命名实例，放在 redis 安装之后发布。
	uuidintSet.Install()
	lockSet.Install()
	// The snapshot is published once every handle of this generation is ready.
	// Publishing it globally serves scripts, tests and code without an entry;
	// HTTP requests do not depend on it — the framework pins this exact
	// snapshot through RequestBinder when it publishes this generation.
	// 快照在本代全部句柄就绪后一次发布。全局发布服务脚本、测试与没有入口的代码；
	// HTTP 请求不依赖它 —— 框架发布本代时通过 RequestBinder 固定的就是这个快照。
	m.snapshot = newSnapshot(dbSet, redisSet, amqpSet, kafkaSet, rocketmqSet, uuidintSet, lockSet)
	Publish(m.snapshot)
	return nil
}

// Snapshot returns the generation this module assembled. Non-HTTP entries —
// scripts, tests, ad-hoc tools — pin it explicitly with WithSnapshot instead of
// reading the process-wide Current(), so they never observe a successor that
// another entry published in the meantime.
// Snapshot 返回本模块组装的那一代快照。非 HTTP 入口（脚本、测试、临时工具）用
// WithSnapshot 显式固定它，而不是读进程级 Current()，因此不会读到别的入口中途
// 发布的后继代。
func (m *Module) Snapshot() *Snapshot { return m.snapshot }

// RequestBinder pins this generation's snapshot into every request served by
// this generation's handler. It is the fix for "handler N + snapshot N+1": the
// framework calls it only for requests that acquired this generation's lease,
// and Start has already produced the snapshot before the generation exists.
//
// The snapshot is captured now rather than read on every request: the module is
// per generation, so the captured value is exactly the one this generation owns.
// A module that was never started returns nil, which leaves the context alone.
// RequestBinder 把本代快照固定进本代 handler 处理的每个请求，这正是
// "handler N + 快照 N+1" 的解法：框架只对拿到本代 lease 的请求调用它，而快照在
// generation 存在之前就已经由 Start 产出。
//
// 快照在这里一次性捕获，而不是每个请求再读一次：模块本就是"每代一个"，捕获到
// 的值就是这一代拥有的那个。尚未启动的模块返回 nil，上下文保持原样。
func (m *Module) RequestBinder() support.RequestBinderFunc {
	snapshot := m.snapshot
	if snapshot == nil {
		return nil
	}
	return func(ctx context.Context) context.Context {
		return WithSnapshot(ctx, snapshot)
	}
}

// Stop closes this generation in reverse opening order: the lock handle, the
// UUID prefetch pool (its ticker reads Redis), the brokers, the Redis clients
// they depend on, and finally the database. A reload starts the replacement
// before stopping the old module, so the old snapshot is withdrawn only when it
// is still the published one and the old handles never clear the new
// connections.
//
// The order is load-bearing, not cosmetic: closing Redis first would leave the
// pool's ticker talking to a closed client until StopPool runs, which turns
// each reload into failed refills and error callbacks.
// Stop 按打开顺序的逆序关闭本代：锁句柄、UUID 预取池（其 ticker 读 Redis）、
// 各 broker、它们依赖的 Redis 客户端，最后是数据库。Reload 先启动新模块再停旧
// 模块，因此只撤回仍指向自己的快照，旧句柄不会清掉新连接。
//
// 这个顺序有实际作用，不是排版：先关 Redis 会让池的 ticker 在 StopPool 之前一直
// 对着已关闭的客户端补货，每次 Reload 都变成失败的重试与错误回调。
func (m *Module) Stop(context.Context) error {
	currentSnapshot.CompareAndSwap(m.snapshot, nil)
	return closeAll(m.closers)
}
