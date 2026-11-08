-- 产品表：demo 中订单服务读取产品名称的真实数据来源（db.default）。
-- 手工执行，可重复运行；不使用 AutoMigrate。
CREATE TABLE IF NOT EXISTS products (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    name VARCHAR(128) NOT NULL,
    price BIGINT NOT NULL,
    stock INT NOT NULL DEFAULT 0,
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 上架，0 下架',
    created_at DATETIME(3) NULL,
    updated_at DATETIME(3) NULL,
    PRIMARY KEY (id),
    KEY idx_products_status (status)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

INSERT IGNORE INTO products (id, name, price, stock, status, created_at, updated_at) VALUES
    (1, 'Gofy T-Shirt', 9900, 120, 1, NOW(3), NOW(3)),
    (2, 'Gofy Mug', 3900, 80, 1, NOW(3), NOW(3)),
    (3, 'Gofy Sticker Pack', 1500, 500, 1, NOW(3), NOW(3)),
    (90001, 'Gofy Demo SKU', 1000, 999, 1, NOW(3), NOW(3));
