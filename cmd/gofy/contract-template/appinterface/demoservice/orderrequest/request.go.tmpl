// Package orderrequest defines the order service requests.
// 包 orderrequest 定义订单服务的请求类型，字段上的 tag 同时声明参数位置、
// 校验规则与 OpenAPI 描述。
package orderrequest

import (
	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/bingcool/gofy-interface-api/contracts/common/types"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
)

// GetOrderRequest queries one order.
// GetOrderRequest 按 ID 查询单个订单，ID 从 URI 路径参数读取且必填。
type GetOrderRequest struct {
	ID types.Uint64String `uri:"id" json:"id" form:"id" validate:"required" message:"required:订单ID不能为空" doc:"订单ID" example:"1234567890123456789"`
}

// ListOrdersRequest pages through orders.
// ListOrdersRequest 分页查询订单，分页参数由内嵌的 paging.PageRequest 提供；
// UserID 必填，Status 可选但只能取已声明的状态。
type ListOrdersRequest struct {
	paging.PageRequest

	UserID types.Uint64String    `form:"user_id" json:"user_id" validate:"required" message:"required:用户ID不能为空" doc:"用户ID" example:"10001"`
	Status orderenum.OrderStatus `form:"status" json:"status" validate:"omitempty,oneof=pending paid cancelled" message:"oneof:订单状态必须是 pending、paid、cancelled" doc:"订单状态" enum:"pending,paid,cancelled"`
	// CreatedFrom/CreatedTo stay strings so query parsing never guesses a timezone.
	// CreatedFrom/CreatedTo 保持字符串类型，避免查询解析擅自推断时区；两者
	// 都按 RFC3339 校验，非法格式由校验器直接拒绝。
	CreatedFrom string `form:"created_from" json:"created_from" validate:"omitempty,datetime=2006-01-02T15:04:05Z07:00" message:"datetime:创建时间起必须是 RFC3339 格式" doc:"创建时间起（RFC3339）"`
	CreatedTo   string `form:"created_to" json:"created_to" validate:"omitempty,datetime=2006-01-02T15:04:05Z07:00" message:"datetime:创建时间止必须是 RFC3339 格式" doc:"创建时间止（RFC3339）"`
}

// CreateOrderItem is one requested line item.
// CreateOrderItem 是请求中的一条订单明细，数量必须大于 0 且不超过 999。
type CreateOrderItem struct {
	SkuID types.Uint64String `json:"sku_id" validate:"required" message:"required:SKU ID不能为空" doc:"SKU ID" example:"90001"`
	Num   int32              `json:"num" validate:"required,gt=0,lte=999" message:"required:数量不能为空,gt:数量必须大于0,lte:数量不能超过999" doc:"数量" example:"2"`
}

// CreateOrderRequest creates an order.
// CreateOrderRequest 创建订单；金额单位为分且必须大于 0，备注上限 255 字符，
// 明细至少 1 条、最多 100 条。
type CreateOrderRequest struct {
	UserID types.Uint64String `json:"user_id" validate:"required" message:"required:用户ID不能为空" doc:"用户ID" example:"10001"`
	Amount int64              `json:"amount" validate:"required,gt=0" message:"required:金额不能为空,gt:金额必须大于0" doc:"金额，单位：分" example:"1999"`
	Remark string             `json:"remark,omitempty" validate:"max=255" message:"max:备注最多255个字符" doc:"备注" example:"尽快发货"`
	Items  []CreateOrderItem  `json:"items" validate:"required,min=1,max=100,dive" message:"required:订单明细不能为空,min:订单明细至少1条,max:订单明细最多100条" doc:"订单明细"`
}

// CancelOrderRequest cancels an order.
// CancelOrderRequest 取消订单，ID 从 URI 路径参数读取且必填。
type CancelOrderRequest struct {
	ID types.Uint64String `uri:"id" json:"id" form:"id" validate:"required" message:"required:订单ID不能为空" doc:"订单ID" example:"1234567890123456789"`
}

// GetConfigRequest queries the order module configuration. It has no fields;
// the type exists so the route descriptor and adapter stay typed.
// GetConfigRequest 查询订单模块配置。该类型没有字段，存在的意义是让路由描述符
// 与 adapter 保持类型安全。
type GetConfigRequest struct{}
