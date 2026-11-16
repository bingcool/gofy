package claim

import (
	"context"
	"errors"
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
	if err := q.Fail(context.Background(), Task{}, nil); !errors.Is(err, ErrOwnerRequired) {
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
		"idx_claim (job, status, next_run_at, id)",
		"ENGINE=InnoDB",
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
	if err := q.Fail(context.Background(), task, errors.New("boom")); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Fail without attempts = %v, want ErrInvalidTask", err)
	}
	if err := q.Succeed(context.Background(), Task{ID: 7, Attempts: 1}); !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("Succeed without owner = %v, want ErrOwnerRequired", err)
	}
}
