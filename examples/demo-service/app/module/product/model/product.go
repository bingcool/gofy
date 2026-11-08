package model

import "time"

// Product is a persisted product.
type Product struct {
	ID        uint64    `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:128" json:"name"`
	Price     int64     `json:"price"` // 单位：分
	Stock     int       `json:"stock"`
	Status    int8      `gorm:"index" json:"status"` // 1 上架，0 下架
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName returns the table backing Product.
func (Product) TableName() string { return "products" }
