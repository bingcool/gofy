-- 认领队列：group1 的 order-settle-consumer 从这张表里认领到期任务。
-- 结构必须与 support/claim 的查询一致，等价于 claim.DDL("job_task")：
-- status 0/1/2/3 = pending/claimed/done/dead，owner 与 attempts 是收尾时的围栏，
-- lease_until 是租约，next_run_at 是到期时间（NULL 表示立刻可认领）。
-- idx_claim 不能省：认领语句是 FOR UPDATE SKIP LOCKED + ORDER BY next_run_at, id，
-- 没有这个索引会退化成全表扫描并持有大量行锁。
-- 时间列用普通 DATETIME（秒精度）：短于一秒的租约或退避会被取整，claim 的 NOW(3)
-- 与两种精度都能正确比较。start_handle_time / finish_handle_time 分别记录行最近一次进入
-- status 1/2 的墙钟时间（Claim、Succeed 写）；fail_handle_time 记录最近一次失败时间，每次
-- Fail 都写（含退避重试回 pending），Reap 判定 dead 也写。created_by / deleted_at 由应用维护，
-- claim 从不读写它们，
-- deleted_at 非空也不会让行从 Claim 里消失。fail_reason 由 Fail 写入、Succeed 清空，
-- 因此 dead 行本身就带着失败原因。rerun_allowed 默认 1，业务插入时按行设置：0 表示这行
-- 只允许执行一次，handler 报错或租约过期都直接转 status 3，不再被任何人重跑。
CREATE TABLE IF NOT EXISTS job_task (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键ID',
    job_type    VARCHAR(64)  NOT NULL COMMENT '任务类型',
    status      TINYINT      NOT NULL DEFAULT 0 COMMENT '任务状态：0待认领(待处理) 1已认领(进行中) 2已完成 3处理失败,待人工介入',
    owner       VARCHAR(96)  NOT NULL DEFAULT '' COMMENT '当前认领者',
    lease_until DATETIME     NULL COMMENT '租约到期时间',
    attempts    INT          NOT NULL DEFAULT 0 COMMENT '已认领次数',
    next_run_at DATETIME     NULL COMMENT '下次可执行时间，NULL表示立即可认领',
    rerun_allowed TINYINT    NOT NULL DEFAULT 1 COMMENT '是否允许被再次消费：1允许(失败重试/租约过期后可被重跑)，0只执行一次(失败或租约过期直接转3)',
    payload     JSON         NULL COMMENT '任务载荷',
    fail_reason TEXT         NULL DEFAULT NULL COMMENT '失败原因',
    start_handle_time  DATETIME NULL DEFAULT NULL COMMENT '状态改为1(已认领进行中)的时间',
    finish_handle_time DATETIME NULL DEFAULT NULL COMMENT '状态改为2(已完成)的时间',
    fail_handle_time   DATETIME NULL DEFAULT NULL COMMENT '最近一次失败时间',
    created_by  VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '创建人',
    created_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    deleted_at  DATETIME     NULL DEFAULT NULL COMMENT '删除时间',
    PRIMARY KEY (id),
    KEY idx_claim (job_type, status, next_run_at, id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '认领任务表';
