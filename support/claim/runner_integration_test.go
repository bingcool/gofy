package claim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These tests drive the full Runner loop against a real MySQL 8 table. They
// verify the promise the unit tests can only inspect the SQL for: a successful
// handler stamps finish_handle_time, a failing one stamps fail_reason +
// fail_handle_time, and the hooks carry the full context.
// 这些测试在真实 MySQL 8 表上跑完整 Runner 循环，验证单测只能从 SQL 字面检查的承诺：成功的
// handler 写 finish_handle_time，失败的写 fail_reason + fail_handle_time，钩子带完整上下文。
func newIntegrationRunner(t *testing.T, cfg RunnerConfig) (*Runner, *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("GOFY_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set GOFY_MYSQL_DSN to run the claim integration tests")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	table := fmt.Sprintf("gofy_claim_runner_%d", time.Now().UnixNano())
	ddl, err := DDL(table)
	if err != nil {
		t.Fatalf("DDL: %v", err)
	}
	if err := db.Exec(ddl).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Exec("DROP TABLE IF EXISTS `" + table + "`").Error; err != nil {
			t.Logf("drop table %s: %v", table, err)
		}
	})
	cfg.Config.Table = table
	r, err := NewRunner(db, cfg)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r, db
}

func TestIntegrationRunnerIdleCallsHandlerWithNilData(t *testing.T) {
	gotNil := false
	success, failure := 0, 0
	r, _ := newIntegrationRunner(t, RunnerConfig{
		Config: Config{Job: "orders", Lease: time.Minute},
		Handler: func(_ context.Context, data []byte) (uint64, error) {
			gotNil = data == nil
			return 42, nil // ignored: no row to settle
		},
		OnSuccess: func(Result) { success++ },
		OnFailure: func(Result) { failure++ },
	})
	if err := r.Run(context.Background(), "worker-a"); err != nil {
		t.Fatalf("Run on empty table: %v", err)
	}
	if !gotNil {
		t.Fatal("idle tick did not call Handler with nil data")
	}
	if success != 0 || failure != 0 {
		t.Fatalf("idle tick fired hooks: success=%d failure=%d, want 0/0", success, failure)
	}
}

func TestIntegrationRunnerSuccessStampsFinishTimeAndHook(t *testing.T) {
	success := 0
	var got Result
	r, db := newIntegrationRunner(t, RunnerConfig{
		Config: Config{Job: "orders", Lease: time.Minute, MaxAttempts: 3},
		Handler: func(_ context.Context, data []byte) (uint64, error) {
			var p struct {
				N uint64 `json:"n"`
			}
			if err := json.Unmarshal(data, &p); err != nil {
				return 0, err
			}
			return p.N, nil
		},
		OnSuccess: func(res Result) { success++; got = res },
		OnFailure: func(Result) { t.Error("OnFailure fired on a successful handler") },
	})
	seed(t, db, r.queue.tableName(), "orders", 1)

	if err := r.Run(context.Background(), "worker-a"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if success != 1 {
		t.Fatalf("OnSuccess fired %d times, want 1", success)
	}
	// seed() numbers rows from 0, so the first row's payload has n=0.
	if got.TaskID != 0 {
		t.Fatalf("Result.TaskID = %d, want 0 from the payload", got.TaskID)
	}
	if got.RowID == 0 || got.Attempts != 1 || got.Err != nil || got.LeaseLost {
		t.Fatalf("unexpected success Result: %+v", got)
	}
	if got.Elapsed <= 0 {
		t.Fatalf("Result.Elapsed = %v, want > 0", got.Elapsed)
	}
	if status := r.queue.statusOf(t, got.RowID); status != StatusDone {
		t.Fatalf("status = %d, want %d (done)", status, StatusDone)
	}
	if !r.queue.timeOf(t, got.RowID, "finish_handle_time").Valid {
		t.Fatal("finish_handle_time not stamped by Runner success")
	}
}

func TestIntegrationRunnerFailureStampsReasonAndTime(t *testing.T) {
	boom := errors.New("settlement unavailable")
	failures := 0
	var got Result
	r, db := newIntegrationRunner(t, RunnerConfig{
		Config: Config{Job: "orders", Lease: time.Minute, MaxAttempts: 3, Backoff: time.Second},
		Handler: func(context.Context, []byte) (uint64, error) {
			return 7, boom
		},
		OnSuccess: func(Result) { t.Error("OnSuccess fired on a failing handler") },
		OnFailure: func(res Result) { failures++; got = res },
	})
	seed(t, db, r.queue.tableName(), "orders", 1)

	if err := r.Run(context.Background(), "worker-a"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if failures != 1 {
		t.Fatalf("OnFailure fired %d times, want 1", failures)
	}
	if !errors.Is(got.Err, boom) || got.Dead || got.LeaseLost || got.TaskID != 7 {
		t.Fatalf("unexpected failure Result: %+v", got)
	}
	if status := r.queue.statusOf(t, got.RowID); status != StatusPending {
		t.Fatalf("status = %d, want %d (retry pending)", status, StatusPending)
	}
	if reason := r.queue.reasonOf(t, got.RowID); reason != boom.Error() {
		t.Fatalf("fail_reason = %q, want %q", reason, boom.Error())
	}
	// Every failure stamps fail_handle_time, including this retryable one that
	// returns the row to pending.
	if !r.queue.timeOf(t, got.RowID, "fail_handle_time").Valid {
		t.Fatal("fail_handle_time not stamped on a retryable failure")
	}
}

func TestIntegrationRunnerDeadPathSetsDeadFlag(t *testing.T) {
	// Seed a row, claim it once so attempts == maxAttempts, then let the Runner
	// claim and fail it at the cap: Fail parks it as dead.
	var got Result
	r, db := newIntegrationRunner(t, RunnerConfig{
		Config: Config{Job: "orders", Lease: time.Minute, MaxAttempts: 1, Backoff: time.Second},
		Handler: func(context.Context, []byte) (uint64, error) {
			return 9, errors.New("last try failed")
		},
		OnFailure: func(res Result) { got = res },
	})
	seed(t, db, r.queue.tableName(), "orders", 1)

	if err := r.Run(context.Background(), "worker-a"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !got.Dead {
		t.Fatal("OnFailure did not set Dead on the final attempt")
	}
	if status := r.queue.statusOf(t, got.RowID); status != StatusDead {
		t.Fatalf("status = %d, want %d (dead)", status, StatusDead)
	}
	if !r.queue.timeOf(t, got.RowID, "fail_handle_time").Valid {
		t.Fatal("fail_handle_time not stamped on dead row")
	}
}

func TestIntegrationRunnerBatchContinuesAfterOneHandlerError(t *testing.T) {
	// One handler always fails; its bookkeeping is written but returns nil from
	// process, so other rows in the same batch must still be settled.
	r, db := newIntegrationRunner(t, RunnerConfig{
		Config: Config{Job: "orders", Lease: time.Minute, MaxAttempts: 3, Batch: 3},
		Handler: func(_ context.Context, data []byte) (uint64, error) {
			var p struct {
				N uint64 `json:"n"`
			}
			_ = json.Unmarshal(data, &p)
			if p.N == 2 {
				return p.N, errors.New("bad row")
			}
			return p.N, nil
		},
	})
	seed(t, db, r.queue.tableName(), "orders", 3)
	if err := r.Run(context.Background(), "worker-a"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var done, pending int64
	if err := db.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE status = %d", r.queue.table, StatusDone)).Scan(&done).Error; err != nil {
		t.Fatalf("count done: %v", err)
	}
	if err := db.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE status = %d", r.queue.table, StatusPending)).Scan(&pending).Error; err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if done != 2 || pending != 1 {
		t.Fatalf("batch outcomes done/pending = %d/%d, want 2/1", done, pending)
	}
}
