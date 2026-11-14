// Package appio wires the demo's named MySQL, Redis, AMQP, Kafka and RocketMQ
// instances into the handles published by their subpackages. It owns the extra
// connections: Start opens and installs them, Stop closes them, and a reload
// rebuilds them together with the module.
//
// The publish/pin mechanics come from support/generation; this package only
// assembles one generation of handles and declares the demo's entry points
// (Middleware, WithContext, and the module lifecycle).
//
// 业务代码通过 Snapshot 取连接：请求中间件在入口固定当前一代，Repository 与
// Service 用 WithContext(ctx) 读取；没有请求上下文时回退到 Current()，两者都没
// 有说明入口没有发布，直接 panic。发布与固定机制由 support/generation 提供，
// 本包只负责组装一代句柄并声明 demo 的入口（Middleware、WithContext、模块
// 生命周期）。
package appio

import (
	"context"
	"errors"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
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
	// 生成器绑定本代 Redis 客户端，取号不再依赖最后安装的包级 Registry。
	uuidintSet, err := appuuidint.Open(m.uuidintCfg, m.rt.Logger, redisSet.Client(m.uuidintCfg.RedisInstanceName()))
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
	// The snapshot is published once every handle of this generation is ready;
	// request middleware takes its connections from it.
	// 快照在本代全部句柄就绪后一次发布；请求中间件从这里取连接。
	m.snapshot = newSnapshot(dbSet, redisSet, amqpSet, kafkaSet, rocketmqSet, uuidintSet)
	Publish(m.snapshot)
	return nil
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
