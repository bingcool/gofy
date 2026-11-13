package model

import "time"

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
