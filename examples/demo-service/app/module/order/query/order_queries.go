package query

import (
	"context"
	"fmt"

	"github.com/bingcool/gofy/support/sqlbuilder"
	"github.com/yourorg/demo-service/app/module/order/model/ordermodel"
)

// OrderLine 是一行订单明细读模型，由 orders ⋈ order_items ⋈ products 三表
// JOIN 得到。它刻意不复用各模块的 model：读模型只需要页面用到的列，字段随
// 查询演进，不参与迁移，也不会把持久化模型带出模块边界。
//
// OrderLine is the read model of one order line produced by a three-table join.
type OrderLine struct {
	OrderID     uint64 `gorm:"column:order_id"`
	UserID      uint64 `gorm:"column:user_id"`
	Status      string `gorm:"column:status"`
	ProductID   uint64 `gorm:"column:product_id"`
	ProductName string `gorm:"column:product_name"`
	Quantity    int    `gorm:"column:quantity"`
	UnitPrice   int64  `gorm:"column:unit_price"` // 分
	LineTotal   int64  `gorm:"column:line_total"` // quantity * unit_price，分
}

// ListOrderLines 返回一张订单的全部明细，按明细 id 升序。
//
// 三张表都是内连接：订单不存在、明细为空、或明细引用了已删除的产品时，对应
// 行不会出现。调用方如果要把“订单存在但没有明细”和“订单不存在”区分开，应
// 先取订单再取明细，而不是从这里的空结果反推。
//
// ListOrderLines returns the lines of one order through a three-table inner join.
func (q *Queries) ListOrderLines(ctx context.Context, orderID uint64) ([]OrderLine, error) {
	db, err := q.conn(ctx)
	if err != nil {
		return nil, err
	}
	const stmt = `
SELECT
    o.id                        AS order_id,
    o.user_id                   AS user_id,
    o.status                    AS status,
    oi.product_id               AS product_id,
    p.name                      AS product_name,
    oi.quantity                 AS quantity,
    oi.unit_price               AS unit_price,
    oi.quantity * oi.unit_price AS line_total
FROM orders o
JOIN order_items oi ON oi.order_id = o.id
JOIN products p     ON p.id = oi.product_id
WHERE o.id = ?
ORDER BY oi.id`
	var rows []OrderLine
	if err := db.Raw(stmt, orderID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("query order lines %d: %w", orderID, err)
	}
	return rows, nil
}

// UnsoldProduct 是从未被任何订单引用过的产品。
//
// UnsoldProduct is a product that no order line references.
type UnsoldProduct struct {
	ID    uint64 `gorm:"column:id"`
	Name  string `gorm:"column:name"`
	Price int64  `gorm:"column:price"` // 分
	Stock int    `gorm:"column:stock"`
}

// ListUnsoldProducts 返回从未出现在订单明细里的产品，按 id 升序。
//
// 用 NOT EXISTS 而不是 LEFT JOIN ... IS NULL：语义相同，但 NOT EXISTS 在
// order_items.product_id 有索引时可以做反半连接，命中一条即可短路，不会先
// 物化整张明细再去重。
//
// ListUnsoldProducts returns products no order line references (NOT EXISTS).
func (q *Queries) ListUnsoldProducts(ctx context.Context, limit int) ([]UnsoldProduct, error) {
	db, err := q.conn(ctx)
	if err != nil {
		return nil, err
	}
	const stmt = `
SELECT p.id, p.name, p.price, p.stock
FROM products p
WHERE NOT EXISTS (
    SELECT 1 FROM order_items oi WHERE oi.product_id = p.id
)
ORDER BY p.id
LIMIT ?`
	var rows []UnsoldProduct
	if err := db.Raw(stmt, clampLimit(limit)).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("query unsold products: %w", err)
	}
	return rows, nil
}

// ListUnsoldProductsWithBuilder is the builder version of ListUnsoldProducts,
// kept beside it as the reference shape for reports gorm's chain API cannot
// express — CTE, UNION, window functions. The two return the same rows and a test
// runs both against one fixture, so the builder path cannot drift from the SQL
// that production uses.
//
// The predicate is built in this function and consumed here: a *Predicate is
// stateful (Query resets it and replays its callbacks), so it must not be cached
// or shared between goroutines. The table names are literals on purpose — this
// package reads other modules' tables directly, so there is no model to ask.
//
// ListUnsoldProductsWithBuilder 是 ListUnsoldProducts 的 builder 版本，与 Raw 版本
// 并列，作为 gorm 链式 API 表达不了的报表（CTE、UNION、窗口函数）的参考写法。两者
// 返回相同的行，测试在同一份 fixture 上同时跑，builder 路径因此不会与生产使用的
// SQL 漂移。
//
// 谓词在本函数内构造、本函数内消费：*Predicate 有状态（Query 会 Reset 并重放回调），
// 不能缓存或跨 goroutine 共享。表名是字面量：本包直接读其他模块的表，没有 model 可问。
func (q *Queries) ListUnsoldProductsWithBuilder(ctx context.Context, limit int) ([]UnsoldProduct, error) {
	db, err := q.conn(ctx)
	if err != nil {
		return nil, err
	}

	products := sqlbuilder.Table("products")
	items := sqlbuilder.Table("order_items")

	// NOT EXISTS (SELECT 1 FROM order_items WHERE order_items.product_id = products.id)
	unused := sqlbuilder.SelectExpr(sqlbuilder.Expr("1")).
		From(items).
		Where(sqlbuilder.ColumnsEQ(items.C("product_id"), products.C("id")))

	stmt, args, err := sqlbuilder.Build(
		sqlbuilder.Select(
			products.C("id"),
			products.C("name"),
			products.C("price"),
			products.C("stock"),
		).
			From(products).
			Where(sqlbuilder.NotExists(unused)).
			OrderBy(products.C("id")).
			Limit(clampLimit(limit)),
	)
	if err != nil {
		return nil, err
	}
	var rows []UnsoldProduct
	if err := db.Raw(stmt, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("query unsold products (builder): %w", err)
	}
	return rows, nil
}

// UserHasOrder 判断用户是否存在指定状态的订单。
//
// 状态由调用方传入（service 的领域常量），query 层不定义业务枚举，避免和
// service 各存一份而漂移。需要“是否存在任意订单”时另写一条不带 status 条件的
// 查询，不要传空字符串——那会变成 status = ” 的静默空结果。
//
// 表名与列名取自 ordermodel，租户条件由 sqlmodel 按模型判定：这条查询因此不必记住
// orders 与它的列名；开启租户隔离后也不会漏掉 tenant_id——谓词由解析器给出，表带租户列
// 而 ctx 没有租户时直接返回错误，而不是悄悄读全表。
//
// 用 EXISTS 而不是 COUNT(*)：命中第一条就返回，不会扫描该用户的全部订单。
//
// UserHasOrder reports whether the user owns at least one order in that status.
func (q *Queries) UserHasOrder(ctx context.Context, userID uint64, status string) (bool, error) {
	db, err := q.conn(ctx)
	if err != nil {
		return false, err
	}

	orders, tenantScope, err := q.models.Table(ctx, ordermodel.Order{})
	if err != nil {
		return false, err
	}
	exists := sqlbuilder.SelectExpr(sqlbuilder.Expr("1")).
		From(orders).
		Where(sqlbuilder.EQ(orders.C(ordermodel.UserID), userID)).
		Where(sqlbuilder.EQ(orders.C(ordermodel.Status), status))
	if tenantScope != nil {
		exists = exists.Where(tenantScope)
	}
	stmt, args, err := sqlbuilder.Build(sqlbuilder.SelectExpr(sqlbuilder.Exists(exists)))
	if err != nil {
		return false, err
	}

	// 一列一行，直接扫进 bool：EXISTS 总是返回一行，不需要区分 sql.ErrNoRows。
	var has bool
	if err := db.Raw(stmt, args...).Row().Scan(&has); err != nil {
		return false, fmt.Errorf("query user %d order status %q: %w", userID, status, err)
	}
	return has, nil
}

// UserOrderStat 是一个用户的订单汇总。
//
// MaxOrderID 刻意不叫 LastOrderID：它由 MAX(id) 得到，含义是"最大的主键"，而不是
// "时间上最近的一笔订单"。自增主键在并发插入、导入历史数据或换成分布式 ID 之后都
// 不再与创建时间同序，一个 last_ 前缀会让人读出错觉。需要时间语义时，按 created_at
// 排序取那一行（配合 (user_id, created_at) 索引），不要用主键代替。
//
// UserOrderStat is the per-user order aggregate.
type UserOrderStat struct {
	UserID      uint64 `gorm:"column:user_id"`
	OrderCount  int64  `gorm:"column:order_count"`
	TotalAmount int64  `gorm:"column:total_amount"` // 分
	MaxOrderID  uint64 `gorm:"column:max_order_id"`
}

// ListUserOrderStats 按用户汇总订单，只返回订单数不少于 minOrders 的用户，
// 按累计金额降序、用户 id 升序。
//
// 这条查询是“原生 SQL 表达力”的样例：GROUP BY + 聚合 + HAVING + 多列排序，
// 用 gorm 的链式 API 写会变成子查询套子查询，而报表场景读 SQL 比读链式调用
// 更直接。表名与列名都是常量，只有阈值走占位符。
//
// ListUserOrderStats aggregates orders per user and filters with HAVING.
func (q *Queries) ListUserOrderStats(ctx context.Context, minOrders, limit int) ([]UserOrderStat, error) {
	db, err := q.conn(ctx)
	if err != nil {
		return nil, err
	}
	if minOrders <= 0 {
		minOrders = 1
	}
	const stmt = `
SELECT
    user_id                  AS user_id,
    COUNT(*)                 AS order_count,
    COALESCE(SUM(amount), 0) AS total_amount,
    MAX(id)                  AS max_order_id
FROM orders
GROUP BY user_id
HAVING COUNT(*) >= ?
ORDER BY total_amount DESC, user_id ASC
LIMIT ?`
	var rows []UserOrderStat
	if err := db.Raw(stmt, minOrders, clampLimit(limit)).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("query user order stats: %w", err)
	}
	return rows, nil
}

// GetUserOrderStat 返回一个用户的订单汇总，没有订单时返回 ErrNotFound。
//
// 单行聚合不能直接 Scan 到结构体再判断零值：用户确实存在但金额为 0 与用户没有
// 订单是两回事。这里先扫到切片再判断长度，把“没有这一行”显式翻译成 ErrNotFound。
//
// GetUserOrderStat returns one user's aggregate or ErrNotFound.
func (q *Queries) GetUserOrderStat(ctx context.Context, userID uint64) (*UserOrderStat, error) {
	db, err := q.conn(ctx)
	if err != nil {
		return nil, err
	}
	const stmt = `
SELECT
    user_id                  AS user_id,
    COUNT(*)                 AS order_count,
    COALESCE(SUM(amount), 0) AS total_amount,
    MAX(id)                  AS max_order_id
FROM orders
WHERE user_id = ?
GROUP BY user_id`
	var rows []UserOrderStat
	if err := db.Raw(stmt, userID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("query user order stat %d: %w", userID, err)
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return &rows[0], nil
}
