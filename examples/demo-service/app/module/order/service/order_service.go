package service

import (
	"context"
	"errors"

	"github.com/yourorg/demo-service/app/module/order/model"
	"github.com/yourorg/demo-service/app/module/order/repository"
	"go.uber.org/zap"
	"gofy/gofy-interface-service/appinterface/demoservice/orderdto"
	"gofy/gofy-interface-service/appinterface/demoservice/orderenum"
	"gofy/gofy-interface-service/contracts/common/types"
)

// ErrOrderNotFound is returned when an order does not exist.
var ErrOrderNotFound = repository.ErrNotFound

// Deps 汇总 OrderService 的依赖。组合多个 repository 时只在这里加字段
// （如 Users user.Repository），构造函数签名保持不变，调用方用字段名显式注入，
// 不必记参数顺序；跨模块的接口由 module.go 装配，service 不自己 new。
type Deps struct {
	// Orders 是订单持久化契约，必填；测试传入自己的实现。
	Orders repository.OrderRepositoryInterface
	// Logger 为 nil 时退化为 zap.NewNop()。
	Logger *zap.Logger
}

// OrderService implements order use cases.
type OrderService struct {
	orders repository.OrderRepositoryInterface
	logger *zap.Logger
}

// NewOrderService builds an OrderService from explicit dependencies.
func NewOrderService(deps Deps) *OrderService {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	logger.Debug("order service initialized", zap.String("component", "order.service"))
	return &OrderService{
		orders: deps.Orders,
		logger: logger.With(zap.String("component", "order.service")),
	}
}

// Get returns one order as a DTO.
func (s *OrderService) Get(ctx context.Context, id uint64) (*orderdto.OrderDTO, error) {
	o, err := s.orders.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	s.logger.Debug("get order", zap.Uint64("order_id", id))
	return toDTO(o), nil
}

// Create persists a pending order and returns it as a DTO.
func (s *OrderService) Create(ctx context.Context, userID uint64, amount int64) (*orderdto.OrderDTO, error) {
	o, err := s.orders.Create(ctx, &model.Order{
		UserID: userID,
		Amount: amount,
		Status: string(orderenum.OrderStatusPending),
	})
	if err != nil {
		return nil, err
	}
	s.logger.Info("order created",
		zap.Uint64("order_id", o.ID),
		zap.Uint64("user_id", userID),
		zap.Int64("amount", amount),
	)
	return toDTO(o), nil
}

// List returns one page of DTOs plus the total count.
func (s *OrderService) List(ctx context.Context, userID uint64, page, size int) ([]orderdto.OrderDTO, int64, error) {
	items, total, err := s.orders.ListByUser(ctx, userID, (page-1)*size, size)
	if err != nil {
		return nil, 0, err
	}
	list := make([]orderdto.OrderDTO, 0, len(items))
	for i := range items {
		list = append(list, *toDTO(&items[i]))
	}
	s.logger.Debug("list orders",
		zap.Uint64("user_id", userID),
		zap.Int("page", page),
		zap.Int("page_size", size),
		zap.Int64("total", total),
	)
	return list, total, nil
}

// Cancel marks an order as cancelled.
func (s *OrderService) Cancel(ctx context.Context, id uint64) error {
	if err := s.orders.UpdateStatus(ctx, id, string(orderenum.OrderStatusCancelled)); err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			// 预期内的业务结果，HTTP 层会映射为 404，不按错误级别记录。
			s.logger.Debug("cancel order: not found", zap.Uint64("order_id", id))
		}
		return err
	}
	s.logger.Info("order cancelled", zap.Uint64("order_id", id))
	return nil
}

// toDTO converts the persistence model at the service boundary.
func toDTO(o *model.Order) *orderdto.OrderDTO {
	return &orderdto.OrderDTO{
		ID:        types.Uint64String(o.ID),
		UserID:    types.Uint64String(o.UserID),
		Amount:    o.Amount,
		Status:    orderenum.OrderStatus(o.Status),
		CreatedAt: o.CreatedAt,
	}
}
