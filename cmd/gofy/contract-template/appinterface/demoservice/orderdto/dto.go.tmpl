// Package orderdto defines the order service data transfer objects.
// 包 orderdto 定义订单服务的数据传输对象（DTO），作为跨服务共享的订单视图。
package orderdto

import (
	"time"

	"github.com/bingcool/gofy-interface-api/contracts/common/types"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
)

// OrderDTO is the cross-service order representation.
// OrderDTO 是跨服务的订单表示；金额字段单位均为分，时间字段为 RFC3339，
// PaidAt 未支付时为 null。
type OrderDTO struct {
	ID        types.Uint64String    `json:"id" doc:"订单ID" example:"1234567890123456789"`
	UserID    types.Uint64String    `json:"user_id" doc:"用户ID" example:"10001"`
	Amount    int64                 `json:"amount" doc:"金额，单位：分" example:"1999"`
	Status    orderenum.OrderStatus `json:"status" doc:"订单状态" enum:"pending,paid,cancelled"`
	Remark    string                `json:"remark,omitempty" doc:"备注" example:"尽快发货"`
	CreatedAt time.Time             `json:"created_at" doc:"创建时间" format:"date-time"`
	PaidAt    *time.Time            `json:"paid_at,omitempty" doc:"支付时间；未支付为 null" format:"date-time"`
}

// OrderItemDTO is one line item of an order.
// OrderItemDTO 是订单中的一条明细行；Price 为单价、Subtotal 为小计，单位都是分。
type OrderItemDTO struct {
	SkuID    types.Uint64String `json:"sku_id" doc:"SKU ID" example:"90001"`
	SkuName  string             `json:"sku_name" doc:"商品名称"`
	Price    int64              `json:"price" doc:"单价，单位：分" example:"1999"`
	Num      int32              `json:"num" doc:"数量" example:"2"`
	Subtotal int64              `json:"subtotal" doc:"小计，单位：分" example:"3998"`
}
