package controller

import (
	"context"
	"errors"
	"time"

	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/bingcool/gofy-interface-api/contracts/common/types"
	gofyadapter "github.com/bingcool/gofy-interface-gofy/adapter/gofy"
	"github.com/bingcool/gofy/apperr"
	"github.com/yourorg/demo-service/app/module/order/service"
	"github.com/yourorg/interface-service/appinterface/demoservice"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderdto"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
	"go.uber.org/zap"
)

var _ demoservice.OrdersService = (*OrderController)(nil)

// OrderController adapts the demoservice contract to OrderService.
// OrderController 实现契约的 Orders 分组：入参转成普通值交给 service，再把领域
// 视图映射回 DTO；controller 不 import model、repository 或 innerapi。
type OrderController struct {
	orderService *service.OrderService
	logger       *zap.Logger
}

// NewOrderController builds a controller around the domain service. A nil
// logger disables logs. Successful requests are left to the access log; the
// controller logs only the unexpected errors it turns into 500 responses,
// including the wrapped cause.
// NewOrderController 绑定订单读写用例，logger 为 nil 时静默。成功请求交给访问
// 日志；controller 只在把错误转成 500 时记 Error 并带上 wrap 的根因，避免与
// 访问日志重复记录同一请求。
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
// GetOrder 返回单个订单；service 的 ErrOrderNotFound 在这里映射成契约的
// ErrOrderNotFound，其余失败按内部错误返回 500。
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
// ListOrders 按用户分页返回订单；页码与页大小原样透传，偏移量换算留在 service，
// 因此 controller 不感知分页语义。
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
// CreateOrder 下单并返回订单视图。SKU 由 service 跨模块调用产品 innerapi 校验；
// 产品不存在、已下架、库存不足都归为调用方错误，统一映射成 400，只有其它异常
// 才记日志并返回 500。
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
// CancelOrder 取消订单；订单不存在映射成契约的 ErrOrderNotFound，其余失败按
// 内部错误返回 500。状态合法性由 service 决定，controller 不做业务判断。
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
