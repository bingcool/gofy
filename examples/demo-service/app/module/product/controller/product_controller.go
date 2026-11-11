// Package controller exposes product handlers for the demo. The in-memory
// samples here are not wired to HTTP routes yet; real product persistence lives
// in app/module/product/repository (products table, db.default) and is consumed
// by the order service through product.innerapi.
package controller

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
)

// ErrProductNotFound is returned when a demo product id does not exist.
var ErrProductNotFound = errors.New("product not found")

// Product is the demo payload returned by the product controller.
type Product struct {
	ID        uint64    `json:"id"`
	Name      string    `json:"name"`
	Price     int64     `json:"price"` // 单位：分
	Stock     int       `json:"stock"`
	CreatedAt time.Time `json:"created_at"`
}

// ProductController serves in-memory sample products.
type ProductController struct {
	logger *zap.Logger
	items  []Product
}

// NewProductController builds a product controller with sample data. A nil
// logger disables logs.
func NewProductController(logger *zap.Logger) *ProductController {
	if logger == nil {
		logger = zap.NewNop()
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &ProductController{
		logger: logger.With(zap.String("component", "product.controller")),
		items: []Product{
			{ID: 1, Name: "Gofy T-Shirt", Price: 9900, Stock: 120, CreatedAt: base},
			{ID: 2, Name: "Gofy Mug", Price: 3900, Stock: 80, CreatedAt: base.Add(time.Hour)},
			{ID: 3, Name: "Gofy Sticker Pack", Price: 1500, Stock: 500, CreatedAt: base.Add(2 * time.Hour)},
		},
	}
}

// Get returns one sample product or ErrProductNotFound.
func (c *ProductController) Get(ctx context.Context, id uint64) (*Product, error) {
	for _, p := range c.items {
		if p.ID == id {
			out := p
			return &out, nil
		}
	}
	return nil, ErrProductNotFound
}

// List returns a copy of the sample products.
func (c *ProductController) List(ctx context.Context) []Product {
	out := make([]Product, len(c.items))
	copy(out, c.items)
	c.logger.Debug("list products", zap.Int("rows", len(out)))
	return out
}
