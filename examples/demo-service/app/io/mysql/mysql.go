// Package mysql exposes the demo's named MySQL connections.
//
// 业务代码直接使用包级句柄：mysql.Db、mysql.OrderDb、mysql.ProductDb。
// Db 就是框架托管的 db.default（Runtime.DB），另外两个由 app/io 包
// 按 db.order、db.product 打开；配置里没有的实例保持 nil。
// 脚本模式不经过模块生命周期，脚本里直接用 script.Runtime.DB。
package mysql

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/bingcool/gofy"
	gofymysql "github.com/bingcool/gofy/support/mysql"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 业务可直接取用的实例句柄；Db 由框架托管，其余实例未配置时为 nil。
var (
	Db        *gorm.DB
	OrderDb   *gorm.DB
	ProductDb *gorm.DB
)

var mu sync.RWMutex

// Set holds one generation of connections. Open builds it, Install publishes it
// and Close releases the extra instances; the default connection belongs to the
// framework and is never closed here.
type Set struct {
	defaultDB *gorm.DB
	registry  *gofymysql.Registry
	order     *gorm.DB
	product   *gorm.DB
}

// Open opens db.order and db.product. defaultDB is the framework-managed
// db.default and is only published, not owned. Unknown instance names are
// rejected so a typo cannot silently leave a handle nil. Extra connections come
// from support/mysql NewIO for this generation.
func Open(ctx context.Context, defaultDB *gorm.DB, cfg map[string]gofy.DatabaseConfig, logger *zap.Logger) (*Set, error) {
	if defaultDB == nil {
		return nil, errors.New("mysql: default database is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	extra := make(map[string]gofymysql.Config, 2)
	for name := range cfg {
		switch name {
		case "default":
			// db.default is opened by the framework; NewIO would create a
			// second pool, so it stays out of the registry.
		case "order", "product":
			extra[name] = cfg[name]
		default:
			return nil, fmt.Errorf("mysql: unknown instance %q (supported: default, order, product)", name)
		}
	}

	registry, err := gofymysql.NewRegistry(extra, logger)
	if err != nil {
		return nil, err
	}

	set := &Set{defaultDB: defaultDB, registry: registry}
	open := func(name string) (*gorm.DB, error) {
		if _, ok := cfg[name]; !ok {
			return nil, nil
		}
		db, err := registry.NewIO(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("mysql instance %q: %w", name, err)
		}
		logger.Info("mysql instance connected", zap.String("instance", name))
		return db, nil
	}

	if set.order, err = open("order"); err != nil {
		_ = registry.Close()
		return nil, err
	}
	if set.product, err = open("product"); err != nil {
		_ = registry.Close()
		return nil, err
	}
	// 全部实例就绪后再切换当前一代：启动失败时旧模块的 NewIO 不受影响。
	registry.Install()
	return set, nil
}

// NewIO returns the pool registered under name in the installed generation.
// It is a thin wrapper over support/mysql. db.default is framework-managed, so
// it is not part of the registry; use Db or Runtime.DB for the default pool.
func NewIO(ctx context.Context, name string) (*gorm.DB, error) {
	return gofymysql.NewIO(ctx, name)
}

// Install publishes the set to the package handles.
func (s *Set) Install() {
	if s == nil {
		return
	}
	mu.Lock()
	Db = s.defaultDB
	OrderDb = s.order
	ProductDb = s.product
	mu.Unlock()
}

// Close releases db.order and db.product. Handles are cleared only when they
// still point at this set, so a reload that already installed new connections
// keeps them intact.
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	mu.Lock()
	if OrderDb == s.order {
		OrderDb = nil
	}
	if ProductDb == s.product {
		ProductDb = nil
	}
	mu.Unlock()

	return s.registry.Close()
}
