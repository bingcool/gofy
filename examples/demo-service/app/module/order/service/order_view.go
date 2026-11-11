package service

import (
	"errors"

	orderapi "github.com/yourorg/demo-service/app/module/order/innerapi"
)

// 领域状态常量：service 与 repository 用这些字符串表达订单状态，不 import
// HTTP 契约枚举。controller 负责 service.Status* ↔ orderenum.OrderStatus 的
// 映射，两侧一致性由 controller 的状态映射测试保证。
const (
	StatusPending   = "pending"
	StatusPaid      = "paid"
	StatusCancelled = "cancelled"
)

// CreateItem 是下单入参，契约类型定义在 innerapi；这里别名导出，方便
// controller 以 service.CreateItem 引用，同时保持 innerapi 不依赖 service。
type CreateItem = orderapi.CreateItem

var (
	// ErrOrderNotFound 表示订单不存在，映射为 HTTP 404。
	ErrOrderNotFound = orderapi.ErrNotFound
	// ErrProductNotFound 表示请求的 SKU 不存在，映射为 HTTP 400。
	ErrProductNotFound = errors.New("product not found")
	// ErrProductUnavailable 表示请求的 SKU 已下架，映射为 HTTP 400。
	ErrProductUnavailable = errors.New("product unavailable")
	// ErrInsufficientStock 表示请求的 SKU 库存不足，映射为 HTTP 400。
	ErrInsufficientStock = errors.New("product stock insufficient")
)
