// Package productmodel holds the persisted model of the products table.
// 包 productmodel 持有 products 表的持久化模型。
package productmodel

import "time"

// Column names, one constant per field. They exist for the queries that spell
// columns as SQL text: there a typo only fails at run time with "unknown
// column", and a rename has to be repeated in every statement.
// 列名，每个字段一个常量。它们服务于把列名写成 SQL 文本的查询：那里拼错要等到运行时
// 才报 "unknown column"，而改一次列名也得把每条语句都改一遍。
const (
	// ID is the products primary key.
	// ID 是 products 表主键。
	ID = "id"
	// Name is the display name shown on the order page.
	// Name 是订单页面展示的商品名称。
	Name = "name"
	// Price is the unit price in cents.
	// Price 是单价，单位为分。
	Price = "price"
	// Stock is the remaining quantity; deduction runs as a conditional update.
	// Stock 是剩余库存；扣减走带条件的 UPDATE。
	Stock = "stock"
	// Status stores 1 for on sale and 0 for off sale.
	// Status 存 1 表示上架、0 表示下架。
	Status = "status"
	// CreatedAt is written by the database on insert.
	// CreatedAt 由数据库在插入时写入。
	CreatedAt = "created_at"
	// UpdatedAt is refreshed by the database on every update.
	// UpdatedAt 由数据库在每次更新时刷新。
	UpdatedAt = "updated_at"
)

// Product is a persisted product.
// Product 是 products 表的持久化模型：价格以分为单位、status 用 int8 存上下架，
// 跨模块视图的 bool OnSale 由 service 换算，仓库与消费方都不直接比较这个状态码。
type Product struct {
	ID        uint64    `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:128" json:"name"`
	Price     int64     `json:"price"` // Unit is cents（单位：分）
	Stock     int       `json:"stock"`
	Status    int8      `gorm:"index" json:"status"` // 1 on sale, 0 off sale（1 上架，0 下架）
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName returns the table backing Product.
// TableName 返回 Product 对应的表名；迁移脚本与产品测试都依赖这张 products 表。
func (Product) TableName() string { return "products" }
