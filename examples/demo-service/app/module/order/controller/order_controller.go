package controller

import (
	"context"
	"errors"

	"github.com/bingcool/gofy/apperr"
	"github.com/yourorg/demo-service/app/module/order/service"
	"go.uber.org/zap"
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
	logger       *zap.Logger
}

// NewOrderController builds a controller around the domain service. A nil
// logger disables logs. Successful requests are left to the access log; the
// controller logs only the unexpected errors it turns into 500 responses,
// including the wrapped cause.
func NewOrderController(svc *service.OrderService, logger *zap.Logger) *OrderController {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OrderController{
		orderService: svc,
		logger:       logger.With(zap.String("component", "order.controller")),
	}
}

// GetOrder returns one order.
func (c *OrderController) GetOrder(ctx context.Context, req *orderrequest.GetOrderRequest) (*orderresponse.GetOrderResponse, error) {
	o, err := c.orderService.Get(ctx, req.ID.Uint64())
	if err != nil {
		if errors.Is(err, service.ErrOrderNotFound) {
			return nil, gofyadapter.Error(demoservice.ErrOrderNotFound)
		}
		c.logger.Error("get order failed", zap.Uint64("order_id", req.ID.Uint64()), zap.Error(err))
		return nil, apperr.Internal("get order failed").WithCause(err)
	}
	return &orderresponse.GetOrderResponse{Order: o}, nil
}

// ListOrders returns one page of orders.
func (c *OrderController) ListOrders(ctx context.Context, req *orderrequest.ListOrdersRequest) (*paging.ListData[orderdto.OrderDTO], error) {
	items, total, err := c.orderService.List(ctx, req.UserID.Uint64(), req.Page, req.PageSize)
	if err != nil {
		c.logger.Error("list orders failed", zap.Uint64("user_id", req.UserID.Uint64()), zap.Error(err))
		return nil, apperr.Internal("list orders failed").WithCause(err)
	}
	return &paging.ListData[orderdto.OrderDTO]{Total: total, List: items}, nil
}

// CreateOrder creates an order and returns it.
func (c *OrderController) CreateOrder(ctx context.Context, req *orderrequest.CreateOrderRequest) (*orderresponse.CreateOrderResponse, error) {
	o, err := c.orderService.Create(ctx, req.UserID.Uint64(), req.Amount)
	if err != nil {
		c.logger.Error("create order failed",
			zap.Uint64("user_id", req.UserID.Uint64()),
			zap.Int64("amount", req.Amount),
			zap.Error(err),
		)
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
		c.logger.Error("cancel order failed", zap.Uint64("order_id", req.ID.Uint64()), zap.Error(err))
		return apperr.Internal("cancel order failed").WithCause(err)
	}
	return nil
}
