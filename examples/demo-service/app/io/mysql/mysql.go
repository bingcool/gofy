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
	order     *gorm.DB
	product   *gorm.DB
}

// Open opens db.order and db.product. defaultDB is the framework-managed
// db.default and is only published, not owned. Unknown instance names are
// rejected so a typo cannot silently leave a handle nil.
func Open(ctx context.Context, defaultDB *gorm.DB, cfg map[string]gofy.DatabaseConfig, logger *zap.Logger) (*Set, error) {
	if defaultDB == nil {
		return nil, errors.New("mysql: default database is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	set := &Set{defaultDB: defaultDB}
	closeOpened := func() {
		_ = gofymysql.Close(set.order)
		_ = gofymysql.Close(set.product)
	}

	for name := range cfg {
		switch name {
		case "default", "order", "product":
		default:
			closeOpened()
			return nil, fmt.Errorf("mysql: unknown instance %q (supported: default, order, product)", name)
		}
	}

	if conf, ok := cfg["order"]; ok {
		db, err := gofymysql.New(ctx, conf, logger)
		if err != nil {
			closeOpened()
			return nil, fmt.Errorf("mysql instance %q: %w", "order", err)
		}
		set.order = db
		logger.Info("mysql instance connected", zap.String("instance", "order"))
	}
	if conf, ok := cfg["product"]; ok {
		db, err := gofymysql.New(ctx, conf, logger)
		if err != nil {
			closeOpened()
			return nil, fmt.Errorf("mysql instance %q: %w", "product", err)
		}
		set.product = db
		logger.Info("mysql instance connected", zap.String("instance", "product"))
	}
	return set, nil
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

	var errs []error
	if err := gofymysql.Close(s.order); err != nil {
		errs = append(errs, fmt.Errorf("mysql instance %q: %w", "order", err))
	}
	if err := gofymysql.Close(s.product); err != nil {
		errs = append(errs, fmt.Errorf("mysql instance %q: %w", "product", err))
	}
	return errors.Join(errs...)
}
