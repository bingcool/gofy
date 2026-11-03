package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/yourorg/order-service/app/module/order/model"
	"gorm.io/gorm"
)

// ErrNotFound is returned when an order does not exist.
var ErrNotFound = errors.New("order not found")

// OrderRepository persists orders with GORM.
type OrderRepository struct {
	db *gorm.DB
}

func New(db *gorm.DB) *OrderRepository {
	return &OrderRepository{db: db}
}

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

func (r *OrderRepository) Create(ctx context.Context, o *model.Order) (*model.Order, error) {
	if err := r.db.WithContext(ctx).Create(o).Error; err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}
	return o, nil
}

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
