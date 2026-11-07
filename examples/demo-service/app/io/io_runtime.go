// Package io wires the demo's named MySQL, Redis, AMQP, Kafka and RocketMQ
// instances into the handles published by their subpackages. It owns the extra
// connections: Start opens and installs them, Stop closes them, and a reload
// rebuilds them together with the module.
//
// 业务代码直接使用 app/io/db、app/io/redis、app/io/amqp、app/io/kafka、
// app/io/rocketmq 的包级句柄，本包只负责生命周期。
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
)

// Module publishes app/io/db, app/io/redis, app/io/amqp, app/io/kafka and
// app/io/rocketmq handles during startup.
type Module struct {
	rt          *gofy.Runtime
	dbCfg       map[string]gofy.DatabaseConfig
	redisCfg    appredis.Config
	amqpCfg     appamqp.Config
	kafkaCfg    appkafka.Config
	rocketmqCfg approcketmq.Config

	dbSet       *appdb.Set
	redisSet    *appredis.Set
	amqpSet     *appamqp.Set
	kafkaSet    *appkafka.Set
	rocketmqSet *approcketmq.Set
}

// NewModuleFactory builds the module from the application runtime.
func NewModuleFactory(dbCfg map[string]gofy.DatabaseConfig, redisCfg appredis.Config, amqpCfg appamqp.Config, kafkaCfg appkafka.Config, rocketmqCfg approcketmq.Config) gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		return NewModule(rt, dbCfg, redisCfg, amqpCfg, kafkaCfg, rocketmqCfg), nil
	}
}

// NewModule builds a module around rt and the instance configuration.
func NewModule(rt *gofy.Runtime, dbCfg map[string]gofy.DatabaseConfig, redisCfg appredis.Config, amqpCfg appamqp.Config, kafkaCfg appkafka.Config, rocketmqCfg approcketmq.Config) *Module {
	return &Module{
		rt:          rt,
		dbCfg:       dbCfg,
		redisCfg:    redisCfg,
		amqpCfg:     amqpCfg,
		kafkaCfg:    kafkaCfg,
		rocketmqCfg: rocketmqCfg,
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
	m.dbSet = dbSet
	m.redisSet = redisSet
	m.amqpSet = amqpSet
	m.kafkaSet = kafkaSet
	m.rocketmqSet = rocketmqSet
	dbSet.Install()
	redisSet.Install()
	amqpSet.Install()
	kafkaSet.Install()
	rocketmqSet.Install()
	return nil
}

// Stop closes this generation. A reload starts the replacement before stopping
// the old module, so the old handles never clear the new connections.
func (m *Module) Stop(context.Context) error {
	return errors.Join(
		m.dbSet.Close(),
		m.redisSet.Close(),
		m.amqpSet.Close(),
		m.kafkaSet.Close(),
		m.rocketmqSet.Close(),
	)
}
