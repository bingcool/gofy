package model

import "time"

// Order is a persisted order.
// Order 是 orders 表的持久化模型：status 存领域状态字符串
// （pending/paid/cancelled），契约枚举与 bool 判断的换算留给 service 和 controller。
type Order struct {
	ID        uint64    `gorm:"primaryKey" json:"id"`
	UserID    uint64    `gorm:"index" json:"user_id"`
	Amount    int64     `json:"amount"`
	Status    string    `gorm:"size:32;index" json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName returns the table backing Order.
// TableName 返回 Order 对应的表名；迁移脚本与订单测试都依赖这张 orders 表。
func (Order) TableName() string { return "orders" }
