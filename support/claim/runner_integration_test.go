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

// TestIntegrationRunnerBatchLeaseHandover covers the full P1 report scenario:
// replica A claims a batch, the tail rows expire while queued, replica B reaps
// and re-claims them, and A must skip those rows instead of running their
// handlers. Every row must be handled exactly once across both replicas.
// TestIntegrationRunnerBatchLeaseHandover 覆盖 P1 报告的完整场景：副本 A 认领一批，尾部行在排队
// 期间过期，副本 B 回收并重新认领；A 必须跳过这些行而不是继续执行 handler。最终每一行在两个
// 副本之间只被执行一次。
func TestIntegrationRunnerBatchLeaseHandover(t *testing.T) {
	handled := 0
	succeeded := map[uint64]bool{}
	skipped := map[uint64]bool{}
	r, db := newIntegrationRunner(t, RunnerConfig{
		Config: Config{Job: "orders", Lease: time.Minute, MaxAttempts: 3, Batch: 3},
		Handler: func(context.Context, []byte) (uint64, error) {
			handled++
			return 0, nil
		},
		OnSuccess: func(res Result) { succeeded[res.RowID] = true },
		OnFailure: func(res Result) {
			if res.LeaseLost {
				skipped[res.RowID] = true
			}
		},
	})
	seed(t, db, r.queue.tableName(), "orders", 3)
	ctx := context.Background()

	// Replica A claims all three rows in one batch.
	// 副本 A 一次认领三行。
	aTasks, err := r.queue.Claim(ctx, "worker-a", 3)
	if err != nil || len(aTasks) != 3 {
		t.Fatalf("A Claim = %v, %v", aTasks, err)
	}

	// The tail rows expire while A is still working on the first one.
	// 尾部两行在 A 还在处理第一行时就过期了。
	if err := db.Exec("UPDATE `"+r.queue.tableName()+
		"` SET lease_until = DATE_SUB(NOW(3), INTERVAL 1 SECOND) WHERE id IN (?, ?)",
		aTasks[1].ID, aTasks[2].ID).Error; err != nil {
		t.Fatalf("expire tail leases: %v", err)
	}

	// Replica B reaps the expired rows and takes them over.
	// 副本 B 回收过期行并接管。
	reaped, dead, err := r.queue.Reap(ctx)
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if reaped != 2 || dead != 0 {
		t.Fatalf("Reap = %d/%d, want 2/0", reaped, dead)
	}
	bTasks, err := r.queue.Claim(ctx, "worker-b", 3)
	if err != nil || len(bTasks) != 2 {
		t.Fatalf("B Claim = %v, %v", bTasks, err)
	}

	// A now walks its batch: row 1 is still owned, rows 2 and 3 must be skipped.
	// A 继续走自己的批次：第 1 行仍持有，第 2、3 行必须跳过。
	for _, task := range aTasks {
		if err := r.process(ctx, task); err != nil {
			t.Fatalf("A process row %d: %v", task.ID, err)
		}
	}
	if handled != 1 {
		t.Fatalf("handled = %d after A, want 1 (only the still-owned row)", handled)
	}
	for _, task := range aTasks[1:] {
		if !skipped[task.ID] {
			t.Fatalf("row %d was not reported as LeaseLost to OnFailure", task.ID)
		}
	}

	// B runs the two rows it owns; now every row has been handled exactly once.
	// B 执行它持有的两行；此时每一行都恰好被执行一次。
	for _, task := range bTasks {
		if err := r.process(ctx, task); err != nil {
			t.Fatalf("B process row %d: %v", task.ID, err)
		}
	}
	if handled != 3 {
		t.Fatalf("handled = %d after B, want 3 (each row exactly once)", handled)
	}
	for _, task := range aTasks {
		if !succeeded[task.ID] {
			t.Fatalf("row %d never succeeded", task.ID)
		}
	}
	if len(succeeded) != 3 {
		t.Fatalf("succeeded rows = %d, want 3 distinct", len(succeeded))
	}
}

// TestIntegrationRunnerSkipsReapedRowBeforeHandler is the end-to-end P1 guard:
// if a claimed row was reaped while waiting in the batch, Runner must skip its
// handler entirely — no side effect may run for a task another replica now owns.
// TestIntegrationRunnerSkipsReapedRowBeforeHandler 是 P1 的端到端守卫：若一批中的某行在排队期间
// 被回收，Runner 必须整个跳过它的 handler——对已被别的副本认领的任务绝不能产生副作用。
func TestIntegrationRunnerSkipsReapedRowBeforeHandler(t *testing.T) {
	handled := 0
	var got Result
	r, db := newIntegrationRunner(t, RunnerConfig{
		Config: Config{Job: "orders", Lease: time.Minute, MaxAttempts: 3},
		Handler: func(context.Context, []byte) (uint64, error) {
			handled++
			return 0, nil
		},
		OnFailure: func(res Result) { got = res },
	})
	seed(t, db, r.queue.tableName(), "orders", 1)

	tasks, err := r.queue.Claim(context.Background(), "worker-a", 1)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Claim = %v, %v", tasks, err)
	}
	// Reap this row behind the handler's back before process() runs.
	// 在 process() 之前偷偷回收这一行。
	if err := db.Exec("UPDATE `"+r.queue.tableName()+
		"` SET status = 0, owner = '', lease_until = NULL WHERE id = ?", tasks[0].ID).Error; err != nil {
		t.Fatalf("reap row: %v", err)
	}

	if err := r.process(context.Background(), tasks[0]); err != nil {
		t.Fatalf("process: %v", err)
	}
	if handled != 0 {
		t.Fatalf("handler ran %d times for a reaped row, want 0 (side effects must not run)", handled)
	}
	if !got.LeaseLost {
		t.Fatal("OnFailure did not report LeaseLost for the skipped row")
	}
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
