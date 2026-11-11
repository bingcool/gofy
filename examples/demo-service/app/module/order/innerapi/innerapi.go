// Package innerapi 是订单模块对其它模块发布的内部 API 契约：接口、只读视图、
// 入参和哨兵错误。不 import 本模块 service/repository/model，也不 import HTTP
// 契约。本轮订单暂无跨模块消费方，契约先行预留，保持与产品模块对称，供后续
// 模块取用。
package innerapi

import (
	"context"
	"errors"
	"time"
)

// Order 是跨模块只读视图：plain struct，不含 model / gorm / 契约类型。
// Status 是领域状态字符串（pending/paid/cancelled），契约枚举只在 controller
// 出现，由 controller 做映射。
type Order struct {
	ID        uint64
	UserID    uint64
	Amount    int64
	Status    string
	CreatedAt time.Time
}

// CreateItem 是下单入参的一行 SKU。定义在契约包里，service 以别名导出，
// controller 用 service.CreateItem 引用，避免 innerapi 反向依赖 service。
type CreateItem struct {
	SkuID uint64
	Num   int32
}

// ErrNotFound 表示订单不存在。
var ErrNotFound = errors.New("order not found")

// OrderAPI 是订单模块对外发布的契约。本模块 service 提供实现。
type OrderAPI interface {
	// Get 返回单个订单视图，不存在时返回 ErrNotFound。
	Get(ctx context.Context, id uint64) (*Order, error)
	// List 按用户分页返回订单视图与总数。
	List(ctx context.Context, userID uint64, page, size int) ([]Order, int64, error)
	// Create 校验 SKU 并落单，返回创建后的订单视图。
	Create(ctx context.Context, userID uint64, amount int64, items []CreateItem) (*Order, error)
	// Cancel 取消订单，不存在时返回 ErrNotFound。
	Cancel(ctx context.Context, id uint64) error
}
