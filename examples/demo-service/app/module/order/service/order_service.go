package service

import (
	"context"
	"time"

	"github.com/yourorg/demo-service/app/module/order/model"
	"github.com/yourorg/demo-service/app/module/order/repository"
	"go.uber.org/zap"
	"gofy/gofy-interface-service/appclient/demoservice/orderenum"
)

// Config holds order service settings.
type Config struct {
	PaymentTimeout time.Duration
}

// Repository is the persistence contract used by OrderService. Tests provide
// their own implementation.
type Repository interface {
	FindByID(ctx context.Context, id uint64) (*model.Order, error)
	Create(ctx context.Context, o *model.Order) (*model.Order, error)
	ListByUser(ctx context.Context, userID uint64, offset, limit int) ([]model.Order, int64, error)
	UpdateStatus(ctx context.Context, id uint64, status string) error
}

// ErrOrderNotFound is returned when an order does not exist.
var ErrOrderNotFound = repository.ErrNotFound

// OrderService implements order use cases.
type OrderService struct {
	repo   Repository
	logger *zap.Logger
	cfg    Config
}

func New(repo Repository, logger *zap.Logger, cfg Config) *OrderService {
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

func (s *OrderService) Get(ctx context.Context, id uint64) (*model.Order, error) {
	return s.repo.FindByID(ctx, id)
}

func (s *OrderService) Create(ctx context.Context, userID uint64, amount int64) (*model.Order, error) {
	s.logger.Debug("create order", zap.Uint64("user_id", userID), zap.Int64("amount", amount))
	return s.repo.Create(ctx, &model.Order{
		UserID: userID,
		Amount: amount,
		Status: string(orderenum.OrderStatusPending),
	})
}

func (s *OrderService) List(ctx context.Context, userID uint64, page, size int) ([]model.Order, int64, error) {
	return s.repo.ListByUser(ctx, userID, (page-1)*size, size)
}

func (s *OrderService) Cancel(ctx context.Context, id uint64) error {
	return s.repo.UpdateStatus(ctx, id, string(orderenum.OrderStatusCancelled))
}
