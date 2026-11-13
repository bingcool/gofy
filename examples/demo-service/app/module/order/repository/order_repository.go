package repository

import (
	"context"
	"errors"
	"fmt"

	gofydb "github.com/bingcool/gofy/support/db"
	appio "github.com/yourorg/demo-service/app/io"
	"github.com/yourorg/demo-service/app/module/order/model"
	"gorm.io/gorm"
)

// ErrNotFound is returned when an order does not exist.
// ErrNotFound 表示订单不存在，service 据此翻译成领域哨兵错误；gorm 的错误类型
// 不出仓库边界。
var ErrNotFound = errors.New("order not found")

// OrderRepository persists orders with GORM. 调用方（service、脚本）直接依赖
// 这个具体类型，不再声明接口：仓库只有一种实现，接口除了让测试塞 fake 之外
// 没有带来替换价值，测试改用纯 Go SQLite 跑真实 SQL。
//
// 仓库不持有连接和日志：每次查询从 ctx 取请求固定的 IO 快照，没有固定值时
// 回退到当前一代；模块工厂早于 app/io.Start 运行也安全，一次请求内的多次
// 查询始终落在同一代连接上。日志由 service 用 rt.Logger 记录。
type OrderRepository struct{}

// conn 优先使用 ctx 中的事务；没有事务时用请求快照的默认数据库。跨模块调用
// 因此自动共享最外层 service 开启的事务。
func (r *OrderRepository) conn(ctx context.Context) *gorm.DB {
	return gofydb.WithContext(ctx, appio.WithContext(ctx).DB())
}

// FindByID returns one order or ErrNotFound.
// FindByID 按主键取一条订单；记录缺失返回 ErrNotFound，其它失败带上订单 id 包装，
// 便于上层定位是哪一条查询出错。
func (r *OrderRepository) FindByID(ctx context.Context, id uint64) (*model.Order, error) {
	var o model.Order
	err := r.conn(ctx).WithContext(ctx).First(&o, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find order %d: %w", id, err)
	}
	return &o, nil
}

// Create inserts o and fills its generated ID.
// Create 插入订单并把自增主键回填到入参 o；事务由 ctx 携带，调用方不用显式传 tx，
// 因此跨模块扣库存与建单能同提交同回滚。
func (r *OrderRepository) Create(ctx context.Context, o *model.Order) (*model.Order, error) {
	if err := r.conn(ctx).WithContext(ctx).Create(o).Error; err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}
	return o, nil
}

// ListByUser returns one page of orders plus the user's total count.
// ListByUser 按用户分页返回订单与总数；计数与列表两条 SQL 都从同一个 ctx 连接
// 发出，保证它们落在同一事务或同一代连接上。
func (r *OrderRepository) ListByUser(ctx context.Context, userID uint64, offset, limit int) ([]model.Order, int64, error) {
	var (
		items []model.Order
		total int64
	)
	q := func() *gorm.DB {
		return r.conn(ctx).WithContext(ctx).Model(&model.Order{}).Where("user_id = ?", userID)
	}
	if err := q().Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count orders: %w", err)
	}
	if err := q().Order("id DESC").Offset(offset).Limit(limit).Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list orders: %w", err)
	}
	return items, total, nil
}

// UpdateStatus sets the status and returns ErrNotFound when the id is missing.
// UpdateStatus 无条件改写订单状态，受影响行数为 0 即 id 不存在，返回 ErrNotFound；
// 状态是否允许从当前值改为目标值由 service 的领域规则决定。
func (r *OrderRepository) UpdateStatus(ctx context.Context, id uint64, status string) error {
	res := r.conn(ctx).WithContext(ctx).Model(&model.Order{}).Where("id = ?", id).Update("status", status)
	if res.Error != nil {
		return fmt.Errorf("update order %d: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// CancelPending moves an order from the from status to the to status and
// reports affected rows. 状态由调用方传入（service 的领域常量），仓库不 import
// HTTP 契约枚举。
func (r *OrderRepository) CancelPending(ctx context.Context, id uint64, from, to string) (int64, error) {
	res := r.conn(ctx).WithContext(ctx).
		Model(&model.Order{}).
		Where("id = ? AND status = ?", id, from).
		Update("status", to)
	if res.Error != nil {
		return 0, fmt.Errorf("cancel pending order %d: %w", id, res.Error)
	}
	return res.RowsAffected, nil
}
