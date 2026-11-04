package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/yourorg/demo-service/app/module/order/model"
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
	db *gorm.DB
}

var _ OrderRepositoryInterface = (*OrderRepository)(nil)

// New builds a GORM-backed order repository.
func New(db *gorm.DB) *OrderRepository {
	return &OrderRepository{db: db}
}

// FindByID returns one order or ErrNotFound.
func (r *OrderRepository) FindByID(ctx context.Context, id uint64) (*model.Order, error) {
	var o model.Order
	err := r.db.WithContext(ctx).First(&o, id).Error
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
	if err := r.db.WithContext(ctx).Create(o).Error; err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}
	return o, nil
}

// ListByUser returns one page of orders plus the user's total count.
func (r *OrderRepository) ListByUser(ctx context.Context, userID uint64, offset, limit int) ([]model.Order, int64, error) {
	var (
		items []model.Order
		total int64
	)
	q := func() *gorm.DB {
		return r.db.WithContext(ctx).Model(&model.Order{}).Where("user_id = ?", userID)
	}
	if err := q().Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count orders: %w", err)
	}
	if err := q().Order("id DESC").Offset(offset).Limit(limit).Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list orders: %w", err)
	}
	return items, total, nil
}

// UpdateStatus sets the status and returns ErrNotFound when the id is missing.
func (r *OrderRepository) UpdateStatus(ctx context.Context, id uint64, status string) error {
	res := r.db.WithContext(ctx).Model(&model.Order{}).Where("id = ?", id).Update("status", status)
	if res.Error != nil {
		return fmt.Errorf("update order %d: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// CancelPending moves a pending order to cancelled and reports affected rows.
func (r *OrderRepository) CancelPending(ctx context.Context, id uint64) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&model.Order{}).
		Where("id = ? AND status = ?", id, string(orderenum.OrderStatusPending)).
		Update("status", string(orderenum.OrderStatusCancelled))
	if res.Error != nil {
		return 0, fmt.Errorf("cancel pending order %d: %w", id, res.Error)
	}
	return res.RowsAffected, nil
}
