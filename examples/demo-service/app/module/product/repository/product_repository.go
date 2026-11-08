package repository

import (
	"context"
	"errors"
	"fmt"

	gofydb "github.com/bingcool/gofy/support/db"
	appdb "github.com/yourorg/demo-service/app/io/db"
	"github.com/yourorg/demo-service/app/module/product/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ErrNotFound is returned when a product does not exist.
var ErrNotFound = errors.New("product not found")

// StatusOnSale marks a product that can be ordered.
const StatusOnSale int8 = 1

// ProductView is the read-only projection other modules may depend on. It keeps
// the persistence model inside the product module while still exposing the
// fields order needs (name, price, stock, availability).
type ProductView struct {
	ID     uint64
	Name   string
	Price  int64
	Stock  int
	Status int8
}

// OnSale reports whether the product can be ordered.
func (v ProductView) OnSale() bool { return v.Status == StatusOnSale }

// ProductRepositoryInterface is the persistence contract used by other modules.
// Tests provide their own implementation.
type ProductRepositoryInterface interface {
	// FindByID returns one product or ErrNotFound.
	FindByID(ctx context.Context, id uint64) (*ProductView, error)
	// ListByIDs returns the products that exist, keyed by id. Missing ids are
	// simply absent from the result.
	ListByIDs(ctx context.Context, ids []uint64) (map[uint64]ProductView, error)
	// DecreaseStock deducts num when the product is on sale and stock is
	// sufficient, returning the affected row count. Zero means the product is
	// missing, off sale or short of stock; the caller maps it to a business error.
	DecreaseStock(ctx context.Context, id uint64, num int) (int64, error)
}

// ProductRepository reads products with GORM.
type ProductRepository struct {
	db     *gorm.DB
	logger *zap.Logger
}

var _ ProductRepositoryInterface = (*ProductRepository)(nil)

// New builds a GORM-backed product repository on an explicit connection.
// Scripts and callers without the app/io lifecycle use this form. A nil logger
// disables logs.
func New(db *gorm.DB, logger *zap.Logger) *ProductRepository {
	return newRepository(db, logger)
}

// NewFromHandle builds a repository backed by db.Db. Module factories run
// before app/io installs the handle, so the lookup happens per query instead of
// at construction; a reload also picks up the replacement connection.
func NewFromHandle(logger *zap.Logger) *ProductRepository {
	return newRepository(nil, logger)
}

func newRepository(db *gorm.DB, logger *zap.Logger) *ProductRepository {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ProductRepository{
		db:     db,
		logger: logger.With(zap.String("component", "product.repository")),
	}
}

// conn 优先使用 ctx 中的事务；没有事务时用显式注入的连接，其次用 app/io
// 安装的 db.Db。跨模块调用因此自动共享最外层 service 开启的事务。
func (r *ProductRepository) conn(ctx context.Context) *gorm.DB {
	fallback := r.db
	if fallback == nil {
		fallback = appdb.Db
	}
	return gofydb.FromContext(ctx, fallback)
}

// FindByID returns one product or ErrNotFound.
func (r *ProductRepository) FindByID(ctx context.Context, id uint64) (*ProductView, error) {
	r.logger.Debug("find product", zap.Uint64("product_id", id))
	var p model.Product
	err := r.conn(ctx).WithContext(ctx).First(&p, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find product %d: %w", id, err)
	}
	return &ProductView{
		ID:     p.ID,
		Name:   p.Name,
		Price:  p.Price,
		Stock:  p.Stock,
		Status: p.Status,
	}, nil
}

// ListByIDs returns the products that exist, keyed by id. Missing ids are
// absent from the result; an empty input returns an empty map without querying.
func (r *ProductRepository) ListByIDs(ctx context.Context, ids []uint64) (map[uint64]ProductView, error) {
	views := make(map[uint64]ProductView, len(ids))
	if len(ids) == 0 {
		return views, nil
	}
	var items []model.Product
	if err := r.conn(ctx).WithContext(ctx).Where("id IN ?", ids).Find(&items).Error; err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	for _, item := range items {
		views[item.ID] = ProductView{
			ID:     item.ID,
			Name:   item.Name,
			Price:  item.Price,
			Stock:  item.Stock,
			Status: item.Status,
		}
	}
	r.logger.Debug("list products",
		zap.Int("requested", len(ids)),
		zap.Int("rows", len(views)),
	)
	return views, nil
}

// DecreaseStock 在商品上架且库存充足时扣减 num，返回受影响行数。条件写在
// SQL 里，避免先查后改的竞争；0 表示商品不存在、已下架或库存不足，由调用方
// 决定映射成哪种业务错误。
func (r *ProductRepository) DecreaseStock(ctx context.Context, id uint64, num int) (int64, error) {
	if num <= 0 {
		return 0, fmt.Errorf("decrease product %d stock: num must be positive", id)
	}
	res := r.conn(ctx).WithContext(ctx).
		Model(&model.Product{}).
		Where("id = ? AND status = ? AND stock >= ?", id, StatusOnSale, num).
		Update("stock", gorm.Expr("stock - ?", num))
	if res.Error != nil {
		return 0, fmt.Errorf("decrease product %d stock: %w", id, res.Error)
	}
	r.logger.Debug("product stock decreased",
		zap.Uint64("product_id", id),
		zap.Int("num", num),
		zap.Int64("rows", res.RowsAffected),
	)
	return res.RowsAffected, nil
}
