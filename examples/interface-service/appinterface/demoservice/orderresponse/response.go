// Package orderresponse defines the order service responses.
// 包 orderresponse 定义订单服务的响应类型，响应体由统一信封包裹后返回。
package orderresponse

import "github.com/yourorg/interface-service/appinterface/demoservice/orderdto"

// GetOrderResponse returns one order.
// GetOrderResponse 返回单个订单详情，订单不存在时由错误信封返回 40401。
type GetOrderResponse struct {
	Order *orderdto.OrderDTO `json:"order" doc:"订单详情"`
}

// CreateOrderResponse returns the created order.
// CreateOrderResponse 返回创建成功后的订单快照，包含服务端生成的 ID。
type CreateOrderResponse struct {
	Order *orderdto.OrderDTO `json:"order" doc:"已创建的订单"`
}

// GetConfigResponse returns the order module configuration.
// GetConfigResponse 返回订单模块配置，目前只有是否要求 Bearer Token 一项。
type GetConfigResponse struct {
	AuthRequired bool `json:"auth_required" doc:"是否要求 Bearer Token"`
}
