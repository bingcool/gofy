package controller

import (
	"context"
	"errors"
	"time"

	"github.com/bingcool/gofy/apperr"
	"github.com/yourorg/demo-service/app/module/order/service"
	"go.uber.org/zap"
	gofyadapter "gofy/gofy-interface-service/adapter/gofy"
	"gofy/gofy-interface-service/appinterface/demoservice"
	"gofy/gofy-interface-service/appinterface/demoservice/orderdto"
	"gofy/gofy-interface-service/appinterface/demoservice/orderenum"
	"gofy/gofy-interface-service/appinterface/demoservice/orderrequest"
	"gofy/gofy-interface-service/appinterface/demoservice/orderresponse"
	"gofy/gofy-interface-service/contracts/common/paging"
	"gofy/gofy-interface-service/contracts/common/types"
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
	return &orderresponse.GetOrderResponse{Order: toOrderDTO(o.ID, o.UserID, o.Amount, o.Status, o.CreatedAt)}, nil
}

// ListOrders returns one page of orders.
func (c *OrderController) ListOrders(ctx context.Context, req *orderrequest.ListOrdersRequest) (*paging.ListData[orderdto.OrderDTO], error) {
	items, total, err := c.orderService.List(ctx, req.UserID.Uint64(), req.Page, req.PageSize)
	if err != nil {
		c.logger.Error("list orders failed", zap.Uint64("user_id", req.UserID.Uint64()), zap.Error(err))
		return nil, apperr.Internal("list orders failed").WithCause(err)
	}
	list := make([]orderdto.OrderDTO, 0, len(items))
	for i := range items {
		list = append(list, *toOrderDTO(items[i].ID, items[i].UserID, items[i].Amount, items[i].Status, items[i].CreatedAt))
	}
	return &paging.ListData[orderdto.OrderDTO]{Total: total, List: list}, nil
}

// CreateOrder creates an order and returns it. SKUs are validated by the
// service against the product module; unknown or off-sale products are client
// errors, not server failures.
func (c *OrderController) CreateOrder(ctx context.Context, req *orderrequest.CreateOrderRequest) (*orderresponse.CreateOrderResponse, error) {
	items := make([]service.CreateItem, 0, len(req.Items))
	for _, item := range req.Items {
		items = append(items, service.CreateItem{SkuID: item.SkuID.Uint64(), Num: item.Num})
	}
	o, err := c.orderService.Create(ctx, req.UserID.Uint64(), req.Amount, items)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrProductNotFound),
			errors.Is(err, service.ErrProductUnavailable),
			errors.Is(err, service.ErrInsufficientStock):
			return nil, apperr.BadRequest(err.Error())
		default:
			c.logger.Error("create order failed",
				zap.Uint64("user_id", req.UserID.Uint64()),
				zap.Int64("amount", req.Amount),
				zap.Error(err),
			)
			return nil, apperr.Internal("create order failed").WithCause(err)
		}
	}
	return &orderresponse.CreateOrderResponse{Order: toOrderDTO(o.ID, o.UserID, o.Amount, o.Status, o.CreatedAt)}, nil
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

// toOrderDTO 把 service 返回的领域视图字段映射成契约 DTO。controller 不 import
// innerapi：视图类型由 service 方法返回值推断，这里只按字段映射——状态字符串
// 转契约枚举、uint64 转 types.Uint64String。状态两侧一致性由 order_status_test.go
// 保证。
func toOrderDTO(id, userID uint64, amount int64, status string, createdAt time.Time) *orderdto.OrderDTO {
	return &orderdto.OrderDTO{
		ID:        types.Uint64String(id),
		UserID:    types.Uint64String(userID),
		Amount:    amount,
		Status:    orderenum.OrderStatus(status),
		CreatedAt: createdAt,
	}
}
