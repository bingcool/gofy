// Package io wires the demo's named MySQL, Redis, AMQP, Kafka and RocketMQ
// instances into the handles published by their subpackages. It owns the extra
// connections: Start opens and installs them, Stop closes them, and a reload
// rebuilds them together with the module.
//
// 业务代码通过 Snapshot 取连接：请求中间件在入口固定当前一代，Repository 与
// Service 用 FromContext(ctx) 读取；没有请求上下文时回退到 Current()。本包只
// 负责生命周期与快照发布。
package io

import (
	"context"
	"errors"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
	appamqp "github.com/yourorg/demo-service/app/io/amqp"
	appdb "github.com/yourorg/demo-service/app/io/db"
	appkafka "github.com/yourorg/demo-service/app/io/kafka"
	appredis "github.com/yourorg/demo-service/app/io/redis"
	approcketmq "github.com/yourorg/demo-service/app/io/rocketmq"
	appuuidint "github.com/yourorg/demo-service/app/io/uuidint"
)

// Module publishes app/io/db, app/io/redis, app/io/amqp, app/io/kafka,
// app/io/rocketmq and app/io/uuidint handles during startup.
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
func NewModuleFactory(dbCfg map[string]gofy.DatabaseConfig, redisCfg appredis.Config, amqpCfg appamqp.Config, kafkaCfg appkafka.Config, rocketmqCfg approcketmq.Config, uuidintCfg appuuidint.Config) gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		return NewModule(rt, dbCfg, redisCfg, amqpCfg, kafkaCfg, rocketmqCfg, uuidintCfg), nil
	}
}

// NewModule builds a module around rt and the instance configuration.
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
func (m *Module) Name() string { return "io" }

// Register is a no-op: this module only publishes connection handles.
func (m *Module) Register(*support.Router) error { return nil }

// Start opens the extra instances and publishes every handle.
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
	// 快照在本代全部句柄就绪后一次发布；请求中间件从这里取连接。
	m.snapshot = newSnapshot(dbSet, redisSet, amqpSet, kafkaSet, rocketmqSet, uuidintSet)
	Publish(m.snapshot)
	return nil
}

// Stop closes this generation. A reload starts the replacement before stopping
// the old module, so the old snapshot is withdrawn only when it is still the
// published one and the old handles never clear the new connections.
func (m *Module) Stop(context.Context) error {
	current.CompareAndSwap(m.snapshot, nil)
	return errors.Join(
		m.dbSet.Close(),
		m.redisSet.Close(),
		m.amqpSet.Close(),
		m.kafkaSet.Close(),
		m.rocketmqSet.Close(),
		m.uuidintSet.Close(),
	)
}
