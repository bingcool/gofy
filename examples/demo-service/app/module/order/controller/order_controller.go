package controller

import (
	"context"
	"errors"

	"github.com/bingcool/gofy/apperr"
	"github.com/yourorg/demo-service/app/module/order/service"
	gofyadapter "gofy/gofy-interface-service/adapter/gofy"
	"gofy/gofy-interface-service/appinterface/demoservice"
	"gofy/gofy-interface-service/appinterface/demoservice/orderdto"
	"gofy/gofy-interface-service/appinterface/demoservice/orderrequest"
	"gofy/gofy-interface-service/appinterface/demoservice/orderresponse"
	"gofy/gofy-interface-service/contracts/common/paging"
)

var _ demoservice.Service = (*OrderController)(nil)

// OrderController adapts the demoservice contract to OrderService.
type OrderController struct {
	orderService *service.OrderService
}

// New builds a controller around the domain service.
func New(svc *service.OrderService) *OrderController {
	return &OrderController{orderService: svc}
}

// GetOrder returns one order.
func (c *OrderController) GetOrder(ctx context.Context, req *orderrequest.GetOrderRequest) (*orderresponse.GetOrderResponse, error) {
	o, err := c.orderService.Get(ctx, req.ID.Uint64())
	if err != nil {
		if errors.Is(err, service.ErrOrderNotFound) {
			return nil, gofyadapter.Error(demoservice.ErrOrderNotFound)
		}
		return nil, apperr.Internal("get order failed").WithCause(err)
	}
	return &orderresponse.GetOrderResponse{Order: o}, nil
}

// ListOrders returns one page of orders.
func (c *OrderController) ListOrders(ctx context.Context, req *orderrequest.ListOrdersRequest) (*paging.ListData[orderdto.OrderDTO], error) {
	items, total, err := c.orderService.List(ctx, req.UserID.Uint64(), req.Page, req.PageSize)
	if err != nil {
		return nil, apperr.Internal("list orders failed").WithCause(err)
	}
	return &paging.ListData[orderdto.OrderDTO]{Total: total, List: items}, nil
}

// CreateOrder creates an order and returns it.
func (c *OrderController) CreateOrder(ctx context.Context, req *orderrequest.CreateOrderRequest) (*orderresponse.CreateOrderResponse, error) {
	o, err := c.orderService.Create(ctx, req.UserID.Uint64(), req.Amount)
	if err != nil {
		return nil, apperr.Internal("create order failed").WithCause(err)
	}
	return &orderresponse.CreateOrderResponse{Order: o}, nil
}

// CancelOrder cancels an order.
func (c *OrderController) CancelOrder(ctx context.Context, req *orderrequest.CancelOrderRequest) error {
	if err := c.orderService.Cancel(ctx, req.ID.Uint64()); err != nil {
		if errors.Is(err, service.ErrOrderNotFound) {
			return gofyadapter.Error(demoservice.ErrOrderNotFound)
		}
		return apperr.Internal("cancel order failed").WithCause(err)
	}
	return nil
}
