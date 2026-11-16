package claim

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New(nil, Config{}); !errors.Is(err, ErrDBRequired) {
		t.Fatalf("New(nil) error = %v, want ErrDBRequired", err)
	}

	db := new(gorm.DB)
	if _, err := New(db, Config{Table: "jobs"}); !errors.Is(err, ErrJobRequired) {
		t.Fatalf("New without job error = %v, want ErrJobRequired", err)
	}

	badTables := []string{"", "jobs;drop table x", "a.b.c", "1jobs", "jobs-archive", "`jobs`"}
	for _, table := range badTables {
		if _, err := New(db, Config{Table: table, Job: "jobs"}); !errors.Is(err, ErrInvalidTable) {
			t.Fatalf("New(table=%q) error = %v, want ErrInvalidTable", table, err)
		}
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	q, err := New(new(gorm.DB), Config{Table: "job_task", Job: "order"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if q.Batch() != DefaultBatch {
		t.Fatalf("Batch() = %d, want %d", q.Batch(), DefaultBatch)
	}
	if q.Lease() != DefaultLease {
		t.Fatalf("Lease() = %v, want %v", q.Lease(), DefaultLease)
	}
	if q.MaxAttempts() != DefaultMaxAttempts {
		t.Fatalf("MaxAttempts() = %d, want %d", q.MaxAttempts(), DefaultMaxAttempts)
	}
	if q.Job() != "order" {
		t.Fatalf("Job() = %q", q.Job())
	}
}

func TestNewRejectsOversizedBatch(t *testing.T) {
	db := new(gorm.DB)
	if _, err := New(db, Config{Table: "job_task", Job: "order", Batch: MaxBatch}); err != nil {
		t.Fatalf("New(batch=%d) should be accepted: %v", MaxBatch, err)
	}
	for _, batch := range []int{MaxBatch + 1, 100, 1000} {
		_, err := New(db, Config{Table: "job_task", Job: "order", Batch: batch})
		if !errors.Is(err, ErrBatchTooLarge) {
			t.Fatalf("New(batch=%d) error = %v, want ErrBatchTooLarge", batch, err)
		}
	}

	// An explicit n bypasses Config, so Claim has to enforce the same ceiling.
	q, err := New(db, Config{Table: "job_task", Job: "order"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := q.Claim(context.Background(), "worker-a", MaxBatch+1); !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("Claim(n=%d) error = %v, want ErrBatchTooLarge", MaxBatch+1, err)
	}
}

func TestNewAcceptsSchemaQualifiedTable(t *testing.T) {
	q, err := New(new(gorm.DB), Config{Table: "app.job_task", Job: "order"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !strings.Contains(q.selectSQL, "`app`.`job_task`") {
		t.Fatalf("selectSQL does not quote the schema and table: %s", q.selectSQL)
	}
}

func TestClaimRequiresOwner(t *testing.T) {
	q, err := New(new(gorm.DB), Config{Table: "job_task", Job: "order"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := q.Claim(context.Background(), "  ", 1); !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("Claim error = %v, want ErrOwnerRequired", err)
	}
	if err := q.Succeed(context.Background(), Task{}); !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("Succeed error = %v, want ErrOwnerRequired", err)
	}
	if _, err := q.Fail(context.Background(), Task{}, nil); !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("Fail error = %v, want ErrOwnerRequired", err)
	}
}

func TestDDLQuotesTableAndAddsClaimIndex(t *testing.T) {
	ddl, err := DDL("job_task")
	if err != nil {
		t.Fatalf("DDL: %v", err)
	}
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS `job_task`",
		"idx_claim (job_type, status, next_run_at, id)",
		"ENGINE=InnoDB",
		"job_type    VARCHAR(64)  NOT NULL COMMENT '任务类型'",
		"fail_reason TEXT         NULL DEFAULT NULL COMMENT '失败原因'",
		"rerun_allowed TINYINT    NOT NULL DEFAULT 1 COMMENT '是否允许被再次消费：1允许(失败重试/租约过期后可被重跑)，0只执行一次(失败或租约过期直接转3)'",
		"start_handle_time  DATETIME NULL DEFAULT NULL COMMENT '状态改为1(已认领进行中)的时间'",
		"finish_handle_time DATETIME NULL DEFAULT NULL COMMENT '状态改为2(已完成)的时间'",
		"fail_handle_time   DATETIME NULL DEFAULT NULL COMMENT '最近一次失败时间'",
		"created_by  VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '创建人'",
		"deleted_at  DATETIME     NULL DEFAULT NULL COMMENT '删除时间'",
		"updated_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间'",
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("DDL does not contain %q:\n%s", want, ddl)
		}
	}
	if strings.Contains(ddl, "DATETIME(3)") || strings.Contains(ddl, "CURRENT_TIMESTAMP(3)") {
		t.Fatalf("DDL still uses millisecond time columns:\n%s", ddl)
	}

	scoped, err := DDL("app.job_task")
	if err != nil {
		t.Fatalf("DDL: %v", err)
	}
	if !strings.Contains(scoped, "`app`.`job_task`") {
		t.Fatalf("DDL does not quote schema: %s", scoped)
	}

	if _, err := DDL("drop table x"); !errors.Is(err, ErrInvalidTable) {
		t.Fatalf("DDL error = %v, want ErrInvalidTable", err)
	}
}

// TestClaimSQLMatchesDDL keeps the copy-paste migration in sync with the DDL the
// package actually expects. Without this guard a column added to DDL (or to the
// SQL) silently leaves the other behind, and a business that copies claim.sql
// builds a table the queries cannot use.
// TestClaimSQLMatchesDDL 让可直接复制的建表 SQL 与包实际依赖的 DDL 保持一致。没有这道守卫，
// 任何一边加列都会悄悄让另一边落后，业务复制 claim.sql 建出的表就跑不通查询。
func TestClaimSQLMatchesDDL(t *testing.T) {
	raw, err := os.ReadFile("claim.sql")
	if err != nil {
		t.Fatalf("read claim.sql: %v", err)
	}
	// Keep only the CREATE statement: drop comment lines and the trailing semicolon.
	var body strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	got := strings.TrimSpace(body.String())
	got = strings.TrimSuffix(got, ";")
	got = strings.TrimSpace(got)

	want, err := DDL("claim_job_task")
	if err != nil {
		t.Fatalf("DDL: %v", err)
	}
	if got != want {
		t.Fatalf("claim.sql drifted from DDL(\"claim_job_task\"):\n--- claim.sql ---\n%s\n--- DDL ---\n%s", got, want)
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	base := 10 * time.Second
	max := 40 * time.Second
	cases := map[int]time.Duration{
		1:  10 * time.Second,
		2:  20 * time.Second,
		3:  40 * time.Second,
		4:  40 * time.Second,
		99: 40 * time.Second,
	}
	for attempts, want := range cases {
		if got := backoffFor(base, max, attempts); got != want {
			t.Fatalf("backoffFor(attempts=%d) = %v, want %v", attempts, got, want)
		}
	}
	if got := backoffFor(base, 5*time.Second, 1); got != 5*time.Second {
		t.Fatalf("backoffFor with max < base = %v, want 5s", got)
	}
}

func TestSQLUsesSkipLockedAndGuards(t *testing.T) {
	q, err := New(new(gorm.DB), Config{Table: "job_task", Job: "order"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !strings.Contains(q.selectSQL, "FOR UPDATE SKIP LOCKED") {
		t.Fatalf("selectSQL lacks SKIP LOCKED: %s", q.selectSQL)
	}
	// Every completion statement is guarded by owner, status and attempts. The
	// attempts value is the per-claim fence: owner + status alone would let the
	// same worker instance write back after its row was reaped and re-claimed.
	// 每条收尾语句都带 owner、status 与 attempts 条件；attempts 才是"每次认领"的
	// 围栏：只有 owner + status 时，同一 worker 实例在行被回收并重新认领后仍能写回。
	for name, sql := range map[string]string{
		"succeed": q.succeedSQL,
		"fail":    q.failSQL,
		"dead":    q.deadSQL,
	} {
		if !strings.Contains(sql, "WHERE id = ? AND owner = ? AND status = 1 AND attempts = ?") {
			t.Fatalf("%sSQL lacks the owner/status/attempts guard: %s", name, sql)
		}
	}
	if !strings.Contains(q.reapSQL, "lease_until < NOW(3)") || !strings.Contains(q.reapSQL, "attempts < ?") {
		t.Fatalf("reapSQL lacks the expiry or attempt guard: %s", q.reapSQL)
	}
	// Time columns: claim -> start, succeed -> finish, and every failure stamps
	// fail_handle_time — both a retryable Fail (back to pending) and the two dead
	// paths. Reap on an expired lease is not a handler failure and stamps none.
	// 时间列：认领写 start、完成写 finish，每次失败都写 fail_handle_time——包括可重试的 Fail
	// （回到 pending）和两条 dead 路径。Reap 回收过期租约不是 handler 失败，不打时间列。
	for name, tc := range map[string]struct{ sql, stamp string }{
		"claim":     {q.updateSQL, "start_handle_time = CURRENT_TIMESTAMP"},
		"succeed":   {q.succeedSQL, "finish_handle_time = CURRENT_TIMESTAMP"},
		"fail":      {q.failSQL, "fail_handle_time = CURRENT_TIMESTAMP"},
		"dead":      {q.deadSQL, "fail_handle_time = CURRENT_TIMESTAMP"},
		"reap dead": {q.reapDeadSQL, "fail_handle_time = CURRENT_TIMESTAMP"},
	} {
		if !strings.Contains(tc.sql, tc.stamp) {
			t.Fatalf("%sSQL does not stamp %q: %s", name, tc.stamp, tc.sql)
		}
	}
	if strings.Contains(q.reapSQL, "_handle_time") {
		t.Fatalf("reapSQL must not stamp a handle-time column: %s", q.reapSQL)
	}
}

// TestExtendIsFencedByOwnerStatusAndAttempts guards the P1 fix: re-arming a
// lease must be a CAS, otherwise a stale handler whose row was reaped could
// extend a row it no longer owns and still run its side effects.
// TestExtendIsFencedByOwnerStatusAndAttempts 守住 P1 修复：续租必须是 CAS，否则行被回收后过期
// handler 仍能给它续租、继续执行副作用。
// TestRerunAllowedSplitsTheQueue guards the at-most-once switch: a row with
// rerun_allowed = 0 must never be rescheduled. Both write paths — a retryable
// Fail and an expired lease — have to park it instead, and the two reap
// statements must stay mutually exclusive on that column.
// TestRerunAllowedSplitsTheQueue 守住 at-most-once 开关：rerun_allowed=0 的行绝不能被重新
// 排期。两条写入路径——可重试的 Fail 与过期租约——都必须改为置死，且两条回收语句要在该列上
// 互斥。
func TestRerunAllowedSplitsTheQueue(t *testing.T) {
	q, err := New(new(gorm.DB), Config{Table: "job_task", Job: "order"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for name, sql := range map[string]string{
		"fail":      q.failSQL,
		"reap":      q.reapSQL,
		"reap dead": q.reapDeadSQL,
	} {
		if !strings.Contains(sql, "rerun_allowed = 1") {
			t.Fatalf("%sSQL must refuse rows that forbid a rerun: %s", name, sql)
		}
	}
	for name, sql := range map[string]string{
		"dead no rerun": q.deadNoRerunSQL,
		"reap no rerun": q.reapNoRerunSQL,
	} {
		if !strings.Contains(sql, "rerun_allowed = 0") {
			t.Fatalf("%sSQL must target only rows that forbid a rerun: %s", name, sql)
		}
	}
	// The no-rerun statements are the fallback after the normal one matched
	// nothing, so they write a reason. deadNoRerun needs the full per-claim
	// fence; reapNoRerun deliberately skips attempts — a row that never reruns
	// has no meaningful attempt count, and the reap statements are already split
	// on rerun_allowed.
	// 两条 no-rerun 语句都是正常语句未命中后的兜底，因此都要写原因。deadNoRerun 需要完整的
	// 每次认领围栏；reapNoRerun 刻意不判断 attempts——不会重跑的行没有有意义的尝试次数，而且
	// 两条回收语句已经按 rerun_allowed 分流了。
	if !strings.Contains(q.deadNoRerunSQL, "status = 1 AND attempts = ?") ||
		!strings.Contains(q.deadNoRerunSQL, "fail_reason = ?") {
		t.Fatalf("deadNoRerunSQL lacks the fence or the reason: %s", q.deadNoRerunSQL)
	}
	if !strings.Contains(q.reapNoRerunSQL, "status = 1") ||
		!strings.Contains(q.reapNoRerunSQL, "fail_reason = ?") {
		t.Fatalf("reapNoRerunSQL lacks the status filter or the reason: %s", q.reapNoRerunSQL)
	}
	if strings.Contains(q.reapNoRerunSQL, "attempts") {
		t.Fatalf("reapNoRerunSQL must not filter on attempts: %s", q.reapNoRerunSQL)
	}
	// Reap must not park a no-rerun row twice: the attempts-based statement only
	// takes rows that allow a rerun.
	if !strings.Contains(q.reapDeadSQL, "attempts >= ?") {
		t.Fatalf("reapDeadSQL lacks the attempts cap: %s", q.reapDeadSQL)
	}
	if strings.Contains(q.reapDeadSQL, "rerun_allowed = 0") {
		t.Fatalf("reapDeadSQL and reapNoRerunSQL overlap: %s", q.reapDeadSQL)
	}
}

func TestExtendIsFencedByOwnerStatusAndAttempts(t *testing.T) {
	q, err := New(new(gorm.DB), Config{Table: "job_task", Job: "order"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, want := range []string{
		"lease_until = DATE_ADD(CURRENT_TIMESTAMP, INTERVAL ? SECOND)",
		"WHERE id = ? AND owner = ? AND status = 1 AND attempts = ?",
	} {
		if !strings.Contains(q.extendSQL, want) {
			t.Fatalf("extendSQL lacks %q: %s", want, q.extendSQL)
		}
	}
}

// TestCompletionRequiresFencingPair rejects hand-built tasks: without the
// attempts value Claim returned, a completion cannot prove it still owns the
// row, so it fails before any statement runs.
// TestCompletionRequiresFencingPair 拒绝手工拼的 task：没有 Claim 返回的 attempts，
// 收尾无法证明自己仍持有该行，因此在执行任何语句之前就失败。
func TestCompletionRequiresFencingPair(t *testing.T) {
	q, err := New(new(gorm.DB), Config{Table: "job_task", Job: "order"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	task := Task{ID: 7, Owner: "worker-a"}
	if err := q.Succeed(context.Background(), task); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Succeed without attempts = %v, want ErrInvalidTask", err)
	}
	if _, err := q.Fail(context.Background(), task, errors.New("boom")); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Fail without attempts = %v, want ErrInvalidTask", err)
	}
	if err := q.Succeed(context.Background(), Task{ID: 7, Attempts: 1}); !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("Succeed without owner = %v, want ErrOwnerRequired", err)
	}
}
