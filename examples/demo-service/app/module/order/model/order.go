package model

import "time"

// Order is a persisted order.
type Order struct {
	ID        uint64    `gorm:"primaryKey" json:"id"`
	UserID    uint64    `gorm:"index" json:"user_id"`
	Amount    int64     `json:"amount"`
	Status    string    `gorm:"size:32;index" json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName returns the table backing Order.
func (Order) TableName() string { return "orders" }
