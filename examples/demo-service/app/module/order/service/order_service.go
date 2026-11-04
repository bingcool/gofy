package service

import (
	"context"
	"time"

	"github.com/yourorg/demo-service/app/module/order/model"
	"github.com/yourorg/demo-service/app/module/order/repository"
	"go.uber.org/zap"
	"gofy/gofy-interface-service/appinterface/demoservice/orderdto"
	"gofy/gofy-interface-service/appinterface/demoservice/orderenum"
	"gofy/gofy-interface-service/contracts/common/types"
)

// Config holds order service settings.
type Config struct {
	PaymentTimeout time.Duration
}

// ErrOrderNotFound is returned when an order does not exist.
var ErrOrderNotFound = repository.ErrNotFound

// OrderService implements order use cases.
type OrderService struct {
	repo   repository.OrderRepositoryInterface
	logger *zap.Logger
	cfg    Config
}

// New builds an OrderService; a nil logger falls back to a no-op logger.
func New(repo repository.OrderRepositoryInterface, logger *zap.Logger, cfg Config) *OrderService {
	if logger == nil {
		logger = zap.NewNop()
	}
	logger.Debug("order service initialized",
		zap.String("component", "order.service"),
		zap.Duration("payment_timeout", cfg.PaymentTimeout),
	)
	return &OrderService{
		repo:   repo,
		logger: logger.With(zap.String("component", "order.service")),
		cfg:    cfg,
	}
}

// Get returns one order as a DTO.
func (s *OrderService) Get(ctx context.Context, id uint64) (*orderdto.OrderDTO, error) {
	o, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return toDTO(o), nil
}

// Create persists a pending order and returns it as a DTO.
func (s *OrderService) Create(ctx context.Context, userID uint64, amount int64) (*orderdto.OrderDTO, error) {
	s.logger.Debug("create order", zap.Uint64("user_id", userID), zap.Int64("amount", amount))
	o, err := s.repo.Create(ctx, &model.Order{
		UserID: userID,
		Amount: amount,
		Status: string(orderenum.OrderStatusPending),
	})
	if err != nil {
		return nil, err
	}
	return toDTO(o), nil
}

// List returns one page of DTOs plus the total count.
func (s *OrderService) List(ctx context.Context, userID uint64, page, size int) ([]orderdto.OrderDTO, int64, error) {
	items, total, err := s.repo.ListByUser(ctx, userID, (page-1)*size, size)
	if err != nil {
		return nil, 0, err
	}
	list := make([]orderdto.OrderDTO, 0, len(items))
	for i := range items {
		list = append(list, *toDTO(&items[i]))
	}
	return list, total, nil
}

// Cancel marks an order as cancelled.
func (s *OrderService) Cancel(ctx context.Context, id uint64) error {
	return s.repo.UpdateStatus(ctx, id, string(orderenum.OrderStatusCancelled))
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
