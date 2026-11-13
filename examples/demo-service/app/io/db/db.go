// Package db exposes the demo's named MySQL-protocol connections.
//
// 业务代码不直接读包级变量：请求内通过 app/io 的快照取连接
// （appio.FromContext(ctx).DB()），无请求上下文时用 Default()。当前一代的
// Set 用 atomic.Pointer 整体发布，读写不再共享可变包变量。
//
// Db 就是框架托管的 db.default（Runtime.DB），order、product 由 app/io 包
// 按 db.order、db.product 在启动时打开；配置里没有的实例保持 nil。
// 其余名字（如 selectDb）注册进 registry，首次 NewIO 时才建连。
// 脚本模式不经过模块生命周期，脚本里用 script.Runtime.DB 发布一个只有
// default 的快照（见 appio.PublishDB）。
package db

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/bingcool/gofy"
	gofydb "github.com/bingcool/gofy/support/db"
	gofyotel "github.com/bingcool/gofy/support/otel"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 已打开的实例名，与配置里的 db.<name> 一一对应。
const (
	InstanceOrder   = "order"
	InstanceProduct = "product"
)

// current 指向当前一代的 Set；发布和读取都不再持有互斥锁。
var current atomic.Pointer[Set]

// Default 返回当前一代的 db.default；尚未安装时为 nil。
func Default() *gorm.DB {
	return current.Load().Default()
}

// Set holds one generation of connections. Open builds it, Install publishes it
// and Close releases the extra instances; the default connection belongs to the
// framework and is never closed here.
type Set struct {
	defaultDB *gorm.DB
	registry  *gofydb.Registry
	order     *gorm.DB
	product   *gorm.DB
}

// NewSet builds a generation that carries only db.default and owns no extra
// pool. It is the script/test path: the caller publishes a pool the framework
// opened instead of running the io module lifecycle.
func NewSet(defaultDB *gorm.DB) *Set {
	return &Set{defaultDB: defaultDB}
}

// Default returns the framework-managed db.default of this generation.
func (s *Set) Default() *gorm.DB {
	if s == nil {
		return nil
	}
	return s.defaultDB
}

// Instance returns the eagerly opened pool for name; nil when the instance is
// not configured in this generation.
func (s *Set) Instance(name string) *gorm.DB {
	if s == nil {
		return nil
	}
	switch name {
	case InstanceOrder:
		return s.order
	case InstanceProduct:
		return s.product
	default:
		return nil
	}
}

// Open opens db.order and db.product eagerly. defaultDB is the framework-managed
// db.default and is only published, not owned. Any other instance name (selectDb
// 等兼容 MySQL 协议的库) is registered for lazy opening via NewIO. Extra
// connections come from support/db NewIO for this generation.
//
// When rt enables tracing, the eager pools also get the GORM span plugin;
// db.default is instrumented by the framework that owns it. Lazy instances are
// not instrumented automatically: 业务取到连接后可自行 db.Use(gofyotel.NewGORMPlugin(...))。
func Open(ctx context.Context, defaultDB *gorm.DB, cfg map[string]gofy.DatabaseConfig, rt *gofy.Runtime) (*Set, error) {
	if defaultDB == nil {
		return nil, errors.New("db: default database is required")
	}
	logger := zap.NewNop()
	if rt != nil && rt.Logger != nil {
		logger = rt.Logger
	}

	extra := make(map[string]gofydb.Config, len(cfg))
	for name, c := range cfg {
		if name == "default" {
			// db.default is opened by the framework; NewIO would create a
			// second pool, so it stays out of the registry.
			continue
		}
		extra[name] = c
	}

	registry, err := gofydb.NewRegistry(extra, logger)
	if err != nil {
		return nil, err
	}

	set := &Set{defaultDB: defaultDB, registry: registry}
	open := func(name string) (*gorm.DB, error) {
		if _, ok := cfg[name]; !ok {
			return nil, nil
		}
		pool, err := registry.NewIO(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("db instance %q: %w", name, err)
		}
		if rt != nil && rt.Tracing {
			if err := pool.Use(gofyotel.NewGORMPlugin(rt.TracerProvider)); err != nil {
				return nil, fmt.Errorf("db instance %q tracing: %w", name, err)
			}
		}
		logger.Info("db instance connected", zap.String("instance", name))
		return pool, nil
	}

	if set.order, err = open(InstanceOrder); err != nil {
		_ = registry.Close()
		return nil, err
	}
	if set.product, err = open(InstanceProduct); err != nil {
		_ = registry.Close()
		return nil, err
	}
	// 全部实例就绪后再切换当前一代：启动失败时旧模块的 NewIO 不受影响。
	registry.Install()
	return set, nil
}

// NewIO returns the pool registered under name in this generation, opening the
// connection on first use. db.default is framework-managed and stays out of the
// registry; use Default for it. A script snapshot built with NewSet owns no
// registry, so it reports ErrNotConfigured.
func (s *Set) NewIO(ctx context.Context, name string) (*gorm.DB, error) {
	if s == nil || s.registry == nil {
		return nil, gofydb.ErrNotConfigured
	}
	return s.registry.NewIO(ctx, name)
}

// NewIO resolves name in the installed generation. It stays for callers that
// have no request context (scripts); request paths go through the snapshot so
// the lookup cannot cross a reload.
func NewIO(ctx context.Context, name string) (*gorm.DB, error) {
	return current.Load().NewIO(ctx, name)
}

// Install publishes this generation. Readers see the whole set or the previous
// one, never a half-updated handle.
func (s *Set) Install() {
	if s == nil {
		return
	}
	current.Store(s)
}

// Close releases every extra pool of this generation. The installed pointer is
// cleared only while it still points at this set, so a reload that already
// installed new connections keeps them intact.
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	current.CompareAndSwap(s, nil)
	if s.registry == nil {
		return nil
	}
	return s.registry.Close()
}
