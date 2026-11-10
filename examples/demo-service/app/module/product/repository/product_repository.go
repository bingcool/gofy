package repository

import (
	"context"
	"errors"
	"fmt"

	gofydb "github.com/bingcool/gofy/support/db"
	appdb "github.com/yourorg/demo-service/app/io/db"
	"github.com/yourorg/demo-service/app/module/product/model"
	"gorm.io/gorm"
)

// NameProducts 是产品仓库在装配注册表里的名字（"<module>.<aggregate>"）。
// 名字由产品模块拥有，登记由消费方模块工厂负责；订单服务按这个名字跨模块
// 取用产品仓库。重命名属于破坏性变更。
const NameProducts = "product.products"

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

// ProductRepository reads and updates products with GORM. 其他模块直接依赖这个
// 具体类型和只读视图 ProductView，不再声明接口：跨模块契约由导出的方法和
// ProductView 承担，持久化模型 model.Product 仍留在本模块内。
//
// 仓库不持有连接和日志：每次查询按需解析 app/io 安装的 db.Db，装配早于
// app/io.Start 也安全，Reload 换连接后自动生效；日志由调用方 service 用
// rt.Logger 记录。
type ProductRepository struct{}

// conn 优先使用 ctx 中的事务；没有事务时用 app/io 安装的 db.Db。跨模块调用
// 因此自动共享最外层 service 开启的事务。
func (r *ProductRepository) conn(ctx context.Context) *gorm.DB {
	return gofydb.FromContext(ctx, appdb.Db)
}

// FindByID returns one product or ErrNotFound.
func (r *ProductRepository) FindByID(ctx context.Context, id uint64) (*ProductView, error) {
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
	return res.RowsAffected, nil
}
