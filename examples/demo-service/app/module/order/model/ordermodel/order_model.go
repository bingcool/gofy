// Package ordermodel holds the persisted model of the orders table.
// 包 ordermodel 持有 orders 表的持久化模型。
package ordermodel

import "time"

// Column names, one constant per field. They exist for the queries that spell
// columns as SQL text: there a typo only fails at run time with "unknown
// column", and a rename has to be repeated in every statement.
// 列名，每个字段一个常量。它们服务于把列名写成 SQL 文本的查询：那里拼错要等到运行时
// 才报 "unknown column"，而改一次列名也得把每条语句都改一遍。
const (
	// ID is the orders primary key.
	// ID 是 orders 表主键。
	ID = "id"
	// UserID is the owner of the order and the key List filters by.
	// UserID 是订单所属用户，也是 List 的过滤键。
	UserID = "user_id"
	// Amount is the order total in cents.
	// Amount 是订单金额，单位为分。
	Amount = "amount"
	// Status holds the domain status string: pending, paid or cancelled.
	// Status 保存领域状态字符串：pending、paid 或 cancelled。
	Status = "status"
	// CreatedAt is written by the database on insert.
	// CreatedAt 由数据库在插入时写入。
	CreatedAt = "created_at"
	// UpdatedAt is refreshed by the database on every update.
	// UpdatedAt 由数据库在每次更新时刷新。
	UpdatedAt = "updated_at"
)

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
