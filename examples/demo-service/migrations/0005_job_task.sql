-- 认领队列：group1 的 order-settle-consumer 从这张表里认领到期任务。
-- 结构必须与 support/claim 的查询一致，等价于 claim.DDL("job_task")：
-- status 0/1/2/3 = pending/claimed/done/dead，owner 与 attempts 是收尾时的围栏，
-- lease_until 是租约，next_run_at 是到期时间（NULL 表示立刻可认领）。
-- idx_claim 不能省：认领语句是 FOR UPDATE SKIP LOCKED + ORDER BY next_run_at, id，
-- 没有这个索引会退化成全表扫描并持有大量行锁。
CREATE TABLE IF NOT EXISTS job_task (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    job         VARCHAR(64)  NOT NULL,
    status      TINYINT      NOT NULL DEFAULT 0,
    owner       VARCHAR(96)  NOT NULL DEFAULT '',
    lease_until DATETIME(3)  NULL,
    attempts    INT          NOT NULL DEFAULT 0,
    next_run_at DATETIME(3)  NULL,
    payload     JSON         NULL,
    created_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
                             ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_claim (job, status, next_run_at, id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;
