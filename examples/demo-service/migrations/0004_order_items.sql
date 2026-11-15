-- 订单明细表：app/query 的 JOIN / EXISTS 样例依赖它。
-- 真实项目里由下单流程与订单表同事务写入；这里只提供读模型需要的列：
-- 行金额单价在写入时冻结（unit_price），后续调价不会改变历史订单。
CREATE TABLE IF NOT EXISTS order_items (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    order_id BIGINT UNSIGNED NOT NULL,
    product_id BIGINT UNSIGNED NOT NULL,
    quantity INT NOT NULL,
    unit_price BIGINT NOT NULL COMMENT '下单时单价，单位分',
    created_at DATETIME(3) NULL,
    updated_at DATETIME(3) NULL,
    PRIMARY KEY (id),
    KEY idx_order_items_order_id (order_id),
    KEY idx_order_items_product_id (product_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- 演示数据用固定的高位 id，避免和自增订单冲突；可重复执行。
INSERT IGNORE INTO orders (id, user_id, amount, status, created_at, updated_at) VALUES
    (900001, 90001, 23700, 'paid', NOW(3), NOW(3)),
    (900002, 90001, 1500, 'pending', NOW(3), NOW(3));

INSERT IGNORE INTO order_items (id, order_id, product_id, quantity, unit_price, created_at, updated_at) VALUES
    (900001, 900001, 1, 2, 9900, NOW(3), NOW(3)),
    (900002, 900001, 2, 1, 3900, NOW(3), NOW(3)),
    (900003, 900002, 3, 1, 1500, NOW(3), NOW(3));
