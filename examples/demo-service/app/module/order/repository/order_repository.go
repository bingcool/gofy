package repository

import (
	"context"
	"errors"
	"fmt"

	appmysql "github.com/yourorg/demo-service/app/io/mysql"
	"github.com/yourorg/demo-service/app/module/order/model"
	"go.uber.org/zap"
	"gofy/gofy-interface-service/appinterface/demoservice/orderenum"
	"gorm.io/gorm"
)

// OrderRepositoryInterface is the persistence contract used by the order
// service. Tests provide their own implementation.
type OrderRepositoryInterface interface {
	// FindByID returns one order or ErrNotFound.
	FindByID(ctx context.Context, id uint64) (*model.Order, error)
	// Create inserts o and fills its generated ID.
	Create(ctx context.Context, o *model.Order) (*model.Order, error)
	// ListByUser returns one page of orders plus the user's total count.
	ListByUser(ctx context.Context, userID uint64, offset, limit int) ([]model.Order, int64, error)
	// UpdateStatus sets the status and returns ErrNotFound when the id is missing.
	UpdateStatus(ctx context.Context, id uint64, status string) error
}

// ErrNotFound is returned when an order does not exist.
var ErrNotFound = errors.New("order not found")

// OrderRepository persists orders with GORM.
type OrderRepository struct {
	db     *gorm.DB
	logger *zap.Logger
}

var _ OrderRepositoryInterface = (*OrderRepository)(nil)

// New builds a GORM-backed order repository on an explicit connection. Scripts
// and callers without the app/io lifecycle use this form. A nil logger disables
// logs. Persistence errors are wrapped and returned, never logged here: the
// service or controller owns the single error log so one failure is not
// recorded twice.
func New(db *gorm.DB, logger *zap.Logger) *OrderRepository {
	return newRepository(db, logger)
}

// NewFromHandle builds a repository backed by mysql.Db. Module factories run
// before app/io installs the handle, so the lookup happens per query instead of
// at construction; a reload also picks up the replacement connection.
func NewFromHandle(logger *zap.Logger) *OrderRepository {
	return newRepository(nil, logger)
}

func newRepository(db *gorm.DB, logger *zap.Logger) *OrderRepository {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OrderRepository{
		db:     db,
		logger: logger.With(zap.String("component", "order.repository")),
	}
}

// conn returns the explicit connection when one was injected, otherwise the
// mysql.Db handle installed by app/io.
func (r *OrderRepository) conn() *gorm.DB {
	if r.db != nil {
		return r.db
	}
	return appmysql.Db
}

// FindByID returns one order or ErrNotFound.
func (r *OrderRepository) FindByID(ctx context.Context, id uint64) (*model.Order, error) {
	r.logger.Debug("find order", zap.Uint64("order_id", id))
	var o model.Order
	err := r.conn().WithContext(ctx).First(&o, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find order %d: %w", id, err)
	}
	return &o, nil
}

// Create inserts o and fills its generated ID.
func (r *OrderRepository) Create(ctx context.Context, o *model.Order) (*model.Order, error) {
	r.logger.Debug("insert order", zap.Uint64("user_id", o.UserID))
	if err := r.conn().WithContext(ctx).Create(o).Error; err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}
	r.logger.Debug("order inserted", zap.Uint64("order_id", o.ID))
	return o, nil
}

// ListByUser returns one page of orders plus the user's total count.
func (r *OrderRepository) ListByUser(ctx context.Context, userID uint64, offset, limit int) ([]model.Order, int64, error) {
	var (
		items []model.Order
		total int64
	)
	q := func() *gorm.DB {
		return r.conn().WithContext(ctx).Model(&model.Order{}).Where("user_id = ?", userID)
	}
	if err := q().Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count orders: %w", err)
	}
	if err := q().Order("id DESC").Offset(offset).Limit(limit).Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list orders: %w", err)
	}
	r.logger.Debug("list orders",
		zap.Uint64("user_id", userID),
		zap.Int("offset", offset),
		zap.Int("limit", limit),
		zap.Int("rows", len(items)),
		zap.Int64("total", total),
	)
	return items, total, nil
}

// UpdateStatus sets the status and returns ErrNotFound when the id is missing.
func (r *OrderRepository) UpdateStatus(ctx context.Context, id uint64, status string) error {
	res := r.conn().WithContext(ctx).Model(&model.Order{}).Where("id = ?", id).Update("status", status)
	if res.Error != nil {
		return fmt.Errorf("update order %d: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	r.logger.Debug("order status updated", zap.Uint64("order_id", id), zap.String("status", status))
	return nil
}

// CancelPending moves a pending order to cancelled and reports affected rows.
func (r *OrderRepository) CancelPending(ctx context.Context, id uint64) (int64, error) {
	res := r.conn().WithContext(ctx).
		Model(&model.Order{}).
		Where("id = ? AND status = ?", id, string(orderenum.OrderStatusPending)).
		Update("status", string(orderenum.OrderStatusCancelled))
	if res.Error != nil {
		return 0, fmt.Errorf("cancel pending order %d: %w", id, res.Error)
	}
	r.logger.Debug("cancel pending order", zap.Uint64("order_id", id), zap.Int64("rows", res.RowsAffected))
	return res.RowsAffected, nil
}
