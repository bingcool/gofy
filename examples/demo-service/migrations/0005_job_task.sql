-- 认领队列：group1 的 order-settle-consumer 从这张表里认领到期任务。
-- 结构必须与 support/claim 的查询一致，等价于 claim.DDL("job_task")：
-- status 0/1/2/3 = pending/claimed/done/dead，owner 与 attempts 是收尾时的围栏，
-- lease_until 是租约，next_run_at 是到期时间（NULL 表示立刻可认领）。
-- idx_claim 不能省：认领语句是 FOR UPDATE SKIP LOCKED + ORDER BY next_run_at, id，
-- 没有这个索引会退化成全表扫描并持有大量行锁。
-- 时间列用普通 DATETIME（秒精度）：短于一秒的租约或退避会被取整，claim 的 NOW(3)
-- 与两种精度都能正确比较。created_by / deleted_at 由应用维护，claim 从不读写它们，
-- deleted_at 非空也不会让行从 Claim 里消失。
CREATE TABLE IF NOT EXISTS job_task (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键ID',
    job         VARCHAR(64)  NOT NULL COMMENT '任务类型',
    status      TINYINT      NOT NULL DEFAULT 0 COMMENT '任务状态：0待认领 1已认领 2已完成 3死亡',
    owner       VARCHAR(96)  NOT NULL DEFAULT '' COMMENT '当前认领者',
    lease_until DATETIME     NULL COMMENT '租约到期时间',
    attempts    INT          NOT NULL DEFAULT 0 COMMENT '已认领次数',
    next_run_at DATETIME     NULL COMMENT '下次可执行时间，NULL表示立即可认领',
    payload     JSON         NULL COMMENT '任务载荷',
    created_by  VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '创建人',
    created_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    deleted_at  DATETIME     NULL DEFAULT NULL COMMENT '删除时间',
    PRIMARY KEY (id),
    KEY idx_claim (job, status, next_run_at, id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '认领任务表';
