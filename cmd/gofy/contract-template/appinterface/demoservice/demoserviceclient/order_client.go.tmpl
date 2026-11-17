package demoserviceclient

import (
	"context"

	"github.com/bingcool/gofy-interface-api/client"
	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/yourorg/interface-service/appinterface/demoservice"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderdto"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderroute"
)

// GetOrder fetches one order.
// GetOrder 按订单 ID 查询单个订单，订单不存在时返回 40401。
func (c *Client) GetOrder(ctx context.Context, req *orderrequest.GetOrderRequest, opts ...client.CallOption) (*orderresponse.GetOrderResponse, error) {
	return call(ctx, c.c, orderroute.GetOrder, req, opts)
}

// ListOrders pages through orders.
// ListOrders 分页查询订单，分页参数与返回结构来自 paging 包，UserID 必填。
func (c *Client) ListOrders(ctx context.Context, req *orderrequest.ListOrdersRequest, opts ...client.CallOption) (*paging.ListData[orderdto.OrderDTO], error) {
	return call(ctx, c.c, orderroute.ListOrders, req, opts)
}

// CreateOrder creates an order.
// CreateOrder 创建订单，明细条数上限为 orderconst.MaxOrderItems（100 条）。
func (c *Client) CreateOrder(ctx context.Context, req *orderrequest.CreateOrderRequest, opts ...client.CallOption) (*orderresponse.CreateOrderResponse, error) {
	return call(ctx, c.c, orderroute.CreateOrder, req, opts)
}

// CancelOrder cancels an order. A successful response has no body.
// CancelOrder 取消订单，成功时返回 204 且无响应体；订单已关闭时返回 409。
func (c *Client) CancelOrder(ctx context.Context, req *orderrequest.CancelOrderRequest, opts ...client.CallOption) error {
	_, err := call(ctx, c.c, orderroute.CancelOrder, req, opts)
	return err
}

// IsOrderNotFound reports whether err carries the order-not-found code.
// IsOrderNotFound 判断 err 是否携带订单不存在错误码（40401）。
func IsOrderNotFound(err error) bool {
	return client.CodeOf(err) == int(demoservice.ErrCodeOrderNotFound)
}

// IsOrderClosed reports whether err carries the order-closed code.
// IsOrderClosed 判断 err 是否携带订单已关闭错误码（40402）。
func IsOrderClosed(err error) bool {
	return client.CodeOf(err) == int(demoservice.ErrCodeOrderClosed)
}
