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

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
	goredis "github.com/redis/go-redis/v9"
	appamqp "github.com/yourorg/demo-service/app/appio/amqp"
	appdb "github.com/yourorg/demo-service/app/appio/db"
	appkafka "github.com/yourorg/demo-service/app/appio/kafka"
	appredis "github.com/yourorg/demo-service/app/appio/redis"
	approcketmq "github.com/yourorg/demo-service/app/appio/rocketmq"
	appuuidint "github.com/yourorg/demo-service/app/appio/uuidint"
)

// Module publishes app/appio/db, app/appio/redis, app/appio/amqp, app/appio/kafka,
// app/appio/rocketmq and app/appio/uuidint handles during startup.
// Module 在启动时打开各实例并发布 db、redis、amqp、kafka、rocketmq、uuidint 的
// 包级句柄；它不注册路由，只负责连接生命周期与快照发布。
type Module struct {
	rt          *gofy.Runtime
	dbCfg       map[string]gofy.DatabaseConfig
	redisCfg    appredis.Config
	amqpCfg     appamqp.Config
	kafkaCfg    appkafka.Config
	rocketmqCfg approcketmq.Config
	uuidintCfg  appuuidint.Config

	dbSet       *appdb.Set
	redisSet    *appredis.Set
	amqpSet     *appamqp.Set
	kafkaSet    *appkafka.Set
	rocketmqSet *approcketmq.Set
	uuidintSet  *appuuidint.Set
	// snapshot 是本代句柄的只读视图，Start 发布、Stop 只撤回仍指向自己的快照。
	snapshot *Snapshot
}

// NewModuleFactory builds the module from the application runtime.
// NewModuleFactory 把实例配置封进模块工厂；每次装配由框架传入当代的 Runtime。
func NewModuleFactory(dbCfg map[string]gofy.DatabaseConfig, redisCfg appredis.Config, amqpCfg appamqp.Config, kafkaCfg appkafka.Config, rocketmqCfg approcketmq.Config, uuidintCfg appuuidint.Config) gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		return NewModule(rt, dbCfg, redisCfg, amqpCfg, kafkaCfg, rocketmqCfg, uuidintCfg), nil
	}
}

// NewModule builds a module around rt and the instance configuration.
// NewModule 用本代 Runtime 与实例配置构造模块；构造函数不建连，连接要到 Start
// 才建立，因此装配失败不会留下半开的连接。
func NewModule(rt *gofy.Runtime, dbCfg map[string]gofy.DatabaseConfig, redisCfg appredis.Config, amqpCfg appamqp.Config, kafkaCfg appkafka.Config, rocketmqCfg approcketmq.Config, uuidintCfg appuuidint.Config) *Module {
	return &Module{
		rt:          rt,
		dbCfg:       dbCfg,
		redisCfg:    redisCfg,
		amqpCfg:     amqpCfg,
		kafkaCfg:    kafkaCfg,
		rocketmqCfg: rocketmqCfg,
		uuidintCfg:  uuidintCfg,
	}
}

// Name returns the module name.
// Name 返回模块名 "io"，框架用它做模块去重与错误标识。
func (m *Module) Name() string { return "io" }

// Register is a no-op: this module only publishes connection handles.
// Register 是空实现：本模块只发布连接句柄，路由由业务模块注册。
func (m *Module) Register(*support.Router) error { return nil }

// Start opens the extra instances and publishes every handle.
// Start 依次打开各实例，任一失败都会关掉已建的连接再返回错误；全部就绪后才
// 安装句柄并一次发布快照，避免请求读到半成品。
func (m *Module) Start(ctx context.Context) error {
	if m.rt == nil {
		return errors.New("io: runtime is required")
	}
	dbSet, err := appdb.Open(ctx, m.rt.DB, m.dbCfg, m.rt)
	if err != nil {
		return err
	}
	redisSet, err := appredis.Open(ctx, m.redisCfg, m.rt)
	if err != nil {
		_ = dbSet.Close()
		return err
	}
	amqpSet, err := appamqp.Open(ctx, m.amqpCfg, m.rt.Logger)
	if err != nil {
		_ = redisSet.Close()
		_ = dbSet.Close()
		return err
	}
	kafkaSet, err := appkafka.Open(ctx, m.kafkaCfg, m.rt.Logger)
	if err != nil {
		_ = amqpSet.Close()
		_ = redisSet.Close()
		_ = dbSet.Close()
		return err
	}
	rocketmqSet, err := approcketmq.Open(ctx, m.rocketmqCfg, m.rt.Logger)
	if err != nil {
		_ = kafkaSet.Close()
		_ = amqpSet.Close()
		_ = redisSet.Close()
		_ = dbSet.Close()
		return err
	}
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
		_ = rocketmqSet.Close()
		_ = kafkaSet.Close()
		_ = amqpSet.Close()
		_ = redisSet.Close()
		_ = dbSet.Close()
		return err
	}
	if err := uuidintSet.Start(ctx); err != nil {
		_ = rocketmqSet.Close()
		_ = kafkaSet.Close()
		_ = amqpSet.Close()
		_ = redisSet.Close()
		_ = dbSet.Close()
		return err
	}
	m.dbSet = dbSet
	m.redisSet = redisSet
	m.amqpSet = amqpSet
	m.kafkaSet = kafkaSet
	m.rocketmqSet = rocketmqSet
	m.uuidintSet = uuidintSet
	dbSet.Install()
	redisSet.Install()
	amqpSet.Install()
	kafkaSet.Install()
	rocketmqSet.Install()
	// uuidint 依赖 redis 的命名实例，放在 redis 安装之后发布。
	uuidintSet.Install()
	// The snapshot is published once every handle of this generation is ready.
	// Publishing it globally serves scripts, tests and code without an entry;
	// HTTP requests do not depend on it — the framework pins this exact
	// snapshot through RequestBinder when it publishes this generation.
	// 快照在本代全部句柄就绪后一次发布。全局发布服务脚本、测试与没有入口的代码；
	// HTTP 请求不依赖它 —— 框架发布本代时通过 RequestBinder 固定的就是这个快照。
	m.snapshot = newSnapshot(dbSet, redisSet, amqpSet, kafkaSet, rocketmqSet, uuidintSet)
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

// Stop closes this generation. A reload starts the replacement before stopping
// the old module, so the old snapshot is withdrawn only when it is still the
// published one and the old handles never clear the new connections.
// Stop 关闭本代全部句柄。Reload 先启动新模块再停旧模块，因此只撤回仍指向自己的
// 快照，旧句柄不会清掉新连接。
func (m *Module) Stop(context.Context) error {
	currentSnapshot.CompareAndSwap(m.snapshot, nil)
	return errors.Join(
		m.dbSet.Close(),
		m.redisSet.Close(),
		m.amqpSet.Close(),
		m.kafkaSet.Close(),
		m.rocketmqSet.Close(),
		m.uuidintSet.Close(),
	)
}
