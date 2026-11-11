// Package innerapi 是产品模块对其它模块发布的内部 API 契约：接口、只读视图和
// 哨兵错误。不 import 本模块 service/repository/model，也不 import HTTP 契约。
// 消费方（如订单 service）只依赖这个包，实现由装配根在构造期注入，提供方
// 内部如何实现对其完全透明。
package innerapi

import (
	"context"
	"errors"
)

// Product 是跨模块只读视图：plain struct，不含 model / gorm / 契约类型。
// OnSale 由 service 从持久化状态换算，消费方不感知产品的状态码定义。
type Product struct {
	ID     uint64
	Name   string
	Price  int64
	Stock  int
	OnSale bool
}

var (
	// ErrNotFound 表示产品不存在。
	ErrNotFound = errors.New("product not found")
	// ErrUnavailable 表示产品已下架，不可下单。
	ErrUnavailable = errors.New("product unavailable")
	// ErrInsufficientStock 表示产品库存不足。
	ErrInsufficientStock = errors.New("product stock insufficient")
)

// ProductAPI 是产品模块对外发布的契约。消费方只依赖这个接口，本模块 service
// 提供实现。
type ProductAPI interface {
	// Get 返回单个产品视图，不存在时返回 ErrNotFound。
	Get(ctx context.Context, id uint64) (*Product, error)
	// ListByIDs 批量返回存在的产品，按 id 索引；缺失的 id 不出现在结果里。
	ListByIDs(ctx context.Context, ids []uint64) (map[uint64]Product, error)
	// DecreaseStock 扣减库存，失败时返回 ErrNotFound / ErrUnavailable /
	// ErrInsufficientStock 之一，调用方据此映射自己的业务错误。
	DecreaseStock(ctx context.Context, id uint64, num int) error
}
