package repository

import (
	"context"
	"errors"
	"fmt"

	gofydb "github.com/bingcool/gofy/support/db"
	"github.com/yourorg/demo-service/app/appio"
	appdb "github.com/yourorg/demo-service/app/appio/db"
	"github.com/yourorg/demo-service/app/module/product/model"
	"gorm.io/gorm"
)

// ErrNotFound is returned when a product does not exist.
// ErrNotFound 表示产品不存在，service 据此翻译成 innerapi 哨兵错误；gorm 的错误
// 类型不出仓库边界。
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
// 仓库不持有连接和日志：每次查询从 ctx 取请求固定的 IO 快照，没有固定值时
// 回退到当前一代；装配早于 app/appio.Start 也安全，一次请求内的多次查询始终
// 落在同一代连接上。日志由调用方 service 用 rt.Logger 记录。
type ProductRepository struct{}

// conn 优先使用 ctx 中的事务；没有事务时用请求快照的默认数据库。跨模块调用
// 因此自动共享最外层 service 开启的事务。
//
// 句柄统一经 DBIO 解析：默认库是框架托管的特例，但名字仍走同一条入口，仓库
// 不需要记住"默认库走另一条路"。
func (r *ProductRepository) conn(ctx context.Context) (*gorm.DB, error) {
	db, err := appio.WithContext(ctx).DBIO(ctx, appdb.DefaultInstance)
	if err != nil {
		return nil, fmt.Errorf("resolve default database: %w", err)
	}
	return gofydb.WithContext(ctx, db), nil
}

// FindByID returns one product or ErrNotFound.
// FindByID 按主键取一条产品；记录缺失返回 ErrNotFound，其它失败带上产品 id 包装，
// 便于上层定位是哪一次查询出错。
func (r *ProductRepository) FindByID(ctx context.Context, id uint64) (*model.Product, error) {
	db, err := r.conn(ctx)
	if err != nil {
		return nil, err
	}
	var p model.Product
	err = db.WithContext(ctx).First(&p, id).Error
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
// ListByIDs 批量取存在的产品并按 id 索引：缺失的 id 直接缺席而非占位，调用方据此
// 判断哪个 SKU 不存在；空入参不发 SQL，直接返回空 map。
func (r *ProductRepository) ListByIDs(ctx context.Context, ids []uint64) (map[uint64]model.Product, error) {
	items := make(map[uint64]model.Product, len(ids))
	if len(ids) == 0 {
		return items, nil
	}
	db, err := r.conn(ctx)
	if err != nil {
		return nil, err
	}
	var rows []model.Product
	if err := db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	for _, row := range rows {
		items[row.ID] = row
	}
	return items, nil
}

// List 返回按 id 倒序的一页产品和总数。列表包含已下架产品，是否可售由
// service 换算成视图的 OnSale，调用方自行过滤。
func (r *ProductRepository) List(ctx context.Context, offset, limit int) ([]model.Product, int64, error) {
	var (
		items []model.Product
		total int64
	)
	db, err := r.conn(ctx)
	if err != nil {
		return nil, 0, err
	}
	q := func() *gorm.DB {
		return db.WithContext(ctx).Model(&model.Product{})
	}
	if err := q().Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count products: %w", err)
	}
	if err := q().Order("id DESC").Offset(offset).Limit(limit).Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list products: %w", err)
	}
	return items, total, nil
}

// DecreaseStock 在商品上架且库存充足时扣减 num，返回受影响行数。条件写在
// SQL 里，避免先查后改的竞争；0 表示商品不存在、已下架或库存不足，由调用方
// service 区分并映射成 innerapi 业务错误。
func (r *ProductRepository) DecreaseStock(ctx context.Context, id uint64, num int) (int64, error) {
	if num <= 0 {
		return 0, fmt.Errorf("decrease product %d stock: num must be positive", id)
	}
	db, err := r.conn(ctx)
	if err != nil {
		return 0, err
	}
	res := db.WithContext(ctx).
		Model(&model.Product{}).
		Where("id = ? AND status = ? AND stock >= ?", id, StatusOnSale, num).
		Update("stock", gorm.Expr("stock - ?", num))
	if res.Error != nil {
		return 0, fmt.Errorf("decrease product %d stock: %w", id, res.Error)
	}
	return res.RowsAffected, nil
}
