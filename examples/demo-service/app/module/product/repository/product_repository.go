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

// ErrNotFound is returned when a product does not exist.
var ErrNotFound = errors.New("product not found")

// StatusOnSale marks a product that can be ordered. 这是持久化层的状态码定义，
// 跨模块视图 innerapi.Product 用 bool OnSale 表达，由 service 换算，消费方不
// 感知这个常量。
const StatusOnSale int8 = 1

// ProductRepository reads and updates products with GORM. 仓库只返回本模块的
// 持久化模型 model.Product，不再导出跨模块视图：跨模块契约由 innerapi 承担，
// model 永远不出模块。仓库不声明接口，只有一种 GORM 实现，测试用纯 Go SQLite
// 跑真实 SQL。
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
func (r *ProductRepository) FindByID(ctx context.Context, id uint64) (*model.Product, error) {
	var p model.Product
	err := r.conn(ctx).WithContext(ctx).First(&p, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find product %d: %w", id, err)
	}
	return &p, nil
}

// ListByIDs returns the products that exist, keyed by id. Missing ids are
// absent from the result; an empty input returns an empty map without querying.
func (r *ProductRepository) ListByIDs(ctx context.Context, ids []uint64) (map[uint64]model.Product, error) {
	items := make(map[uint64]model.Product, len(ids))
	if len(ids) == 0 {
		return items, nil
	}
	var rows []model.Product
	if err := r.conn(ctx).WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	for _, row := range rows {
		items[row.ID] = row
	}
	return items, nil
}

// DecreaseStock 在商品上架且库存充足时扣减 num，返回受影响行数。条件写在
// SQL 里，避免先查后改的竞争；0 表示商品不存在、已下架或库存不足，由调用方
// service 区分并映射成 innerapi 业务错误。
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
