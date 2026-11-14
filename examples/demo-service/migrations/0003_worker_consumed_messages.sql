-- 消费幂等台账：每个 (consumer, message_id) 一行。
-- 唯一索引就是去重机制：认领消息是一次 INSERT，要么拿到行，要么得知已存在，
-- 两个副本之间没有读改写窗口。status 取值 processing / done / failed / dead。
-- 消费者首次启动时也会用 AutoMigrate 建这张表，此脚本供 schema 变更走发布流程的
-- 环境使用，两边 DDL 一致。
CREATE TABLE IF NOT EXISTS worker_consumed_messages (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    consumer VARCHAR(64) NOT NULL,
    message_id VARCHAR(128) NOT NULL,
    status VARCHAR(16) NOT NULL,
    attempts BIGINT NOT NULL DEFAULT 0,
    last_error VARCHAR(512) NULL,
    created_at DATETIME(3) NULL,
    updated_at DATETIME(3) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_worker_consumed (consumer, message_id),
    KEY idx_worker_consumed_status (status)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;
