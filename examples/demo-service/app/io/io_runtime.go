// Package io wires the demo's named MySQL and Redis instances into the handles
// published by its mysql and redis subpackages. It owns the extra connections:
// Start opens and installs them, Stop closes them, and a reload rebuilds them
// together with the module.
//
// 业务代码直接使用 app/io/mysql、app/io/redis 的包级句柄，本包只负责生命周期。
package io

import (
	"context"
	"errors"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/transport"
	appmysql "github.com/yourorg/demo-service/app/io/mysql"
	appredis "github.com/yourorg/demo-service/app/io/redis"
)

// Module publishes app/io/mysql and app/io/redis handles during startup.
type Module struct {
	rt       *gofy.Runtime
	dbCfg    map[string]gofy.DatabaseConfig
	redisCfg appredis.Config

	mysqlSet *appmysql.Set
	redisSet *appredis.Set
}

// NewModuleFactory builds the module from the application runtime.
func NewModuleFactory(dbCfg map[string]gofy.DatabaseConfig, redisCfg appredis.Config) gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (transport.Module, error) {
		return NewModule(rt, dbCfg, redisCfg), nil
	}
}

// NewModule builds a module around rt and the instance configuration.
func NewModule(rt *gofy.Runtime, dbCfg map[string]gofy.DatabaseConfig, redisCfg appredis.Config) *Module {
	return &Module{rt: rt, dbCfg: dbCfg, redisCfg: redisCfg}
}

// Name returns the module name.
func (m *Module) Name() string { return "io" }

// Register is a no-op: this module only publishes connection handles.
func (m *Module) Register(*transport.Router) error { return nil }

// Start opens the extra instances and publishes every handle.
func (m *Module) Start(ctx context.Context) error {
	if m.rt == nil {
		return errors.New("io: runtime is required")
	}
	mysqlSet, err := appmysql.Open(ctx, m.rt.DB, m.dbCfg, m.rt.Logger)
	if err != nil {
		return err
	}
	redisSet, err := appredis.Open(ctx, m.redisCfg, m.rt.Logger)
	if err != nil {
		_ = mysqlSet.Close()
		return err
	}
	m.mysqlSet = mysqlSet
	m.redisSet = redisSet
	mysqlSet.Install()
	redisSet.Install()
	return nil
}

// Stop closes this generation. A reload starts the replacement before stopping
// the old module, so the old handles never clear the new connections.
func (m *Module) Stop(context.Context) error {
	return errors.Join(m.mysqlSet.Close(), m.redisSet.Close())
}
