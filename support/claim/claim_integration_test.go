package claim

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// MySQL 8 is required for FOR UPDATE SKIP LOCKED, so these tests talk to a real
// server and skip when GOFY_MYSQL_DSN is unset. Each test creates its own table
// and drops it afterwards; nothing is written to tables the application owns.
// 这些测试需要真实的 MySQL 8（FOR UPDATE SKIP LOCKED），未设置 GOFY_MYSQL_DSN 时
// 跳过。每个用例自建表并在结束后删除，不会写应用自己的表。
func newIntegrationQueue(t *testing.T, cfg Config) (*Queue, *gorm.DB) {
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
	table := fmt.Sprintf("gofy_claim_test_%d", time.Now().UnixNano())
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
	cfg.Table = table
	q, err := New(db, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return q, db
}

func seed(t *testing.T, db *gorm.DB, table, job string, n int) {
	t.Helper()
	// One multi-row INSERT per chunk: seeding 1000 rows one statement at a time
	// dominates the test runtime otherwise.
	// 每 200 行一条多值 INSERT：逐条插入会让 1000 行种子的耗时压过测试本身。
	const chunk = 200
	for start := 0; start < n; start += chunk {
		end := start + chunk
		if end > n {
			end = n
		}
		rows := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*2)
		for i := start; i < end; i++ {
			rows = append(rows, "(?, 0, JSON_OBJECT('n', ?))")
			args = append(args, job, i)
		}
		stmt := "INSERT INTO `" + table + "` (job_type, status, payload) VALUES " + strings.Join(rows, ",")
		if err := db.Exec(stmt, args...).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func TestIntegrationClaimIsExclusive(t *testing.T) {
	q, db := newIntegrationQueue(t, Config{
		Job:         "orders",
		Batch:       7,
		Lease:       30 * time.Second,
		MaxAttempts: 3,
	})
	seed(t, db, q.tableName(), "orders", 1000)
	seed(t, db, q.tableName(), "other", 5)

	var (
		mu   sync.Mutex
		seen = map[uint64]int{}
		wg   sync.WaitGroup
	)
	for _, owner := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			for {
				tasks, err := q.Claim(context.Background(), owner, 7)
				if err != nil {
					t.Errorf("Claim: %v", err)
					return
				}
				if len(tasks) == 0 {
					return
				}
				mu.Lock()
				for _, task := range tasks {
					seen[task.ID]++
				}
				mu.Unlock()
			}
		}(owner)
	}
	wg.Wait()

	if len(seen) != 1000 {
		t.Fatalf("claimed %d distinct rows, want 1000", len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("row %d claimed %d times, want 1", id, count)
		}
	}

	var untouched int64
	if err := db.Raw("SELECT COUNT(*) FROM `" + q.tableName() + "` WHERE job_type = 'other' AND status = 0").
		Scan(&untouched).Error; err != nil {
		t.Fatalf("count other: %v", err)
	}
	if untouched != 5 {
		t.Fatalf("other job rows = %d, want 5 untouched", untouched)
	}
}

// TestIntegrationExtendRejectsReapedRow is the P1 regression: a handler that
// finds its row was reaped while it waited must not proceed. Extend succeeds
// while the claim is live and fails with ErrLeaseLost once another replica has
// returned the row to pending (the state Reap leaves behind).
// TestIntegrationExtendRejectsReapedRow 是 P1 回归：handler 在排队期间发现行被回收就不能继续。
// 认领有效时 Extend 成功；一旦别的副本把行放回 pending（Reap 留下的状态），Extend 返回 ErrLeaseLost。
func TestIntegrationExtendRejectsReapedRow(t *testing.T) {
	q, db := newIntegrationQueue(t, Config{
		Job:   "orders",
		Lease: time.Minute,
	})
	seed(t, db, q.tableName(), "orders", 1)

	tasks, err := q.Claim(context.Background(), "worker-a", 1)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Claim = %v, %v", tasks, err)
	}
	if err := q.Extend(context.Background(), tasks[0]); err != nil {
		t.Fatalf("Extend on a live claim: %v", err)
	}

	// Simulate Reap: status back to pending, lease cleared, attempts unchanged.
	// 模拟 Reap：状态回到 pending、租约清空、attempts 不变。
	if err := db.Exec("UPDATE `"+q.tableName()+
		"` SET status = 0, owner = '', lease_until = NULL WHERE id = ?", tasks[0].ID).Error; err != nil {
		t.Fatalf("reap row: %v", err)
	}
	if err := q.Extend(context.Background(), tasks[0]); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Extend after reap = %v, want ErrLeaseLost", err)
	}
}

func TestIntegrationSucceedCAS(t *testing.T) {
	lost := 0
	q, _ := newIntegrationQueue(t, Config{
		Job:   "orders",
		Lease: 30 * time.Second,
		Hooks: Hooks{OnLeaseLost: func(string) { lost++ }},
	})
	seed(t, q.db, q.tableName(), "orders", 1)

	tasks, err := q.Claim(context.Background(), "worker-a", 1)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Claim = %v, %v", tasks, err)
	}
	task := tasks[0]

	if err := q.Succeed(context.Background(), Task{ID: task.ID, Owner: "worker-b", Attempts: task.Attempts}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Succeed as other owner error = %v, want ErrLeaseLost", err)
	}
	if lost != 1 {
		t.Fatalf("OnLeaseLost fired %d times, want 1", lost)
	}
	if err := q.Succeed(context.Background(), task); err != nil {
		t.Fatalf("Succeed: %v", err)
	}
	if got := q.statusOf(t, task.ID); got != StatusDone {
		t.Fatalf("status = %d, want %d", got, StatusDone)
	}
	if !q.timeOf(t, task.ID, "start_handle_time").Valid {
		t.Fatal("start_handle_time not stamped on Claim")
	}
	if !q.timeOf(t, task.ID, "finish_handle_time").Valid {
		t.Fatal("finish_handle_time not stamped on Succeed")
	}
	if q.timeOf(t, task.ID, "fail_handle_time").Valid {
		t.Fatal("fail_handle_time must stay NULL for a done row")
	}
}

func TestIntegrationFailSchedulesRetryThenDead(t *testing.T) {
	q, db := newIntegrationQueue(t, Config{
		Job:         "orders",
		Lease:       30 * time.Second,
		MaxAttempts: 2,
		Backoff:     200 * time.Millisecond,
	})
	seed(t, db, q.tableName(), "orders", 1)

	tasks, err := q.Claim(context.Background(), "worker-a", 1)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Claim = %v, %v", tasks, err)
	}
	if _, err := q.Fail(context.Background(), tasks[0], errors.New("boom")); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if got := q.reasonOf(t, tasks[0].ID); got != "boom" {
		t.Fatalf("fail_reason after retry = %q, want %q", got, "boom")
	}
	if !q.timeOf(t, tasks[0].ID, "fail_handle_time").Valid {
		t.Fatal("fail_handle_time not stamped on a retryable failure")
	}

	// The retry delay has not elapsed, so the row must stay invisible.
	// 重试延迟未到，这一行必须仍不可见。
	if next, err := q.Claim(context.Background(), "worker-a", 1); err != nil || len(next) != 0 {
		t.Fatalf("immediate Claim = %v, %v; want none", next, err)
	}

	// A sub-second backoff is rounded up to one whole second by ceilSeconds,
	// because the DDL stores DATETIME (second precision). Wait past that floor.
	// 亚秒退避会被 ceilSeconds 向上取整到一秒，因为 DDL 存的是 DATETIME（秒精度）。等待要
	// 越过这个下限。
	time.Sleep(2200 * time.Millisecond)
	tasks, err = q.Claim(context.Background(), "worker-b", 1)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("delayed Claim = %v, %v", tasks, err)
	}
	if tasks[0].Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", tasks[0].Attempts)
	}

	if _, err := q.Fail(context.Background(), tasks[0], errors.New("boom again")); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if got := q.statusOf(t, tasks[0].ID); got != StatusDead {
		t.Fatalf("status = %d, want %d (dead)", got, StatusDead)
	}
	if got := q.reasonOf(t, tasks[0].ID); got != "boom again" {
		t.Fatalf("fail_reason after dead = %q, want %q", got, "boom again")
	}
	if !q.timeOf(t, tasks[0].ID, "fail_handle_time").Valid {
		t.Fatal("fail_handle_time not stamped when Fail moves the row to dead")
	}

	reaped, dead, err := q.Reap(context.Background())
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if reaped != 0 || dead != 0 {
		t.Fatalf("Reap = %d/%d, want 0/0 for a dead row", reaped, dead)
	}
}

func TestIntegrationSucceedClearsFailureReason(t *testing.T) {
	q, db := newIntegrationQueue(t, Config{
		Job:         "orders",
		Lease:       30 * time.Second,
		MaxAttempts: 3,
		Backoff:     200 * time.Millisecond,
	})
	seed(t, db, q.tableName(), "orders", 1)

	tasks, err := q.Claim(context.Background(), "worker-a", 1)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Claim = %v, %v", tasks, err)
	}
	if _, err := q.Fail(context.Background(), tasks[0], errors.New("transient")); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if got := q.reasonOf(t, tasks[0].ID); got != "transient" {
		t.Fatalf("fail_reason after Fail = %q, want transient", got)
	}

	// A sub-second backoff is rounded up to one whole second by ceilSeconds,
	// because the DDL stores DATETIME (second precision). Wait past that floor.
	// 亚秒退避会被 ceilSeconds 向上取整到一秒，因为 DDL 存的是 DATETIME（秒精度）。等待要
	// 越过这个下限。
	time.Sleep(2200 * time.Millisecond)
	tasks, err = q.Claim(context.Background(), "worker-a", 1)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("delayed Claim = %v, %v", tasks, err)
	}
	if err := q.Succeed(context.Background(), tasks[0]); err != nil {
		t.Fatalf("Succeed: %v", err)
	}
	if got := q.reasonOf(t, tasks[0].ID); got != "" {
		t.Fatalf("fail_reason after Succeed = %q, want empty", got)
	}
}

func TestIntegrationReapReturnsExpiredLease(t *testing.T) {
	q, db := newIntegrationQueue(t, Config{
		Job:         "orders",
		Lease:       time.Minute,
		MaxAttempts: 3,
	})
	seed(t, db, q.tableName(), "orders", 1)

	tasks, err := q.Claim(context.Background(), "worker-a", 1)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Claim = %v, %v", tasks, err)
	}
	if err := db.Exec("UPDATE `"+q.tableName()+
		"` SET lease_until = DATE_SUB(NOW(3), INTERVAL 1 SECOND) WHERE id = ?", tasks[0].ID).Error; err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	reaped, dead, err := q.Reap(context.Background())
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if reaped != 1 || dead != 0 {
		t.Fatalf("Reap = %d/%d, want 1/0", reaped, dead)
	}

	again, err := q.Claim(context.Background(), "worker-b", 1)
	if err != nil || len(again) != 1 {
		t.Fatalf("Claim after reap = %v, %v", again, err)
	}
	if again[0].Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", again[0].Attempts)
	}
}

// A row with rerun_allowed = 0 may run once. Its first failure is not scheduled
// for a retry — it goes straight to dead even though attempts is far below the
// cap, and no worker can claim it afterwards.
// rerun_allowed=0 的行只允许跑一次。它的第一次失败不会被排期重试，而是直接置死——即使
// attempts 远未到上限——之后也没有 worker 能再认领它。
func TestIntegrationFailParksNoRerunRow(t *testing.T) {
	q, db := newIntegrationQueue(t, Config{
		Job:         "orders",
		Lease:       time.Minute,
		MaxAttempts: 3,
	})
	seed(t, db, q.tableName(), "orders", 1)

	tasks, err := q.Claim(context.Background(), "worker-a", 1)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Claim = %v, %v", tasks, err)
	}
	// Set the switch after the claim: Fail reads the column, Claim does not, so
	// the first execution is the one that gets parked.
	// 认领之后再打开开关：Fail 读该列而 Claim 不读，因此被置死的是这第一次执行。
	if err := db.Exec("UPDATE `"+q.tableName()+
		"` SET rerun_allowed = 0 WHERE id = ?", tasks[0].ID).Error; err != nil {
		t.Fatalf("mark no-rerun: %v", err)
	}

	dead, err := q.Fail(context.Background(), tasks[0], errors.New("boom"))
	if err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if !dead {
		t.Fatal("Fail scheduled a retry for a row that forbids reruns")
	}
	if got := q.statusOf(t, tasks[0].ID); got != StatusDead {
		t.Fatalf("status = %d, want %d", got, StatusDead)
	}
	if got := q.reasonOf(t, tasks[0].ID); got != "boom" {
		t.Fatalf("fail_reason = %q, want %q", got, "boom")
	}
	if !q.timeOf(t, tasks[0].ID, "fail_handle_time").Valid {
		t.Fatal("fail_handle_time not stamped")
	}

	again, err := q.Claim(context.Background(), "worker-b", 1)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("a no-rerun row was claimed again: %+v", again)
	}
}

// Reap must split an expired batch on rerun_allowed: the row that forbids a
// rerun is parked with its own reason, the one that allows it goes back to
// pending and is claimable again. This is the at-most-once half of the switch.
// Reap 必须按 rerun_allowed 拆分一批过期行：禁止重跑的行带着自己的原因被置死，允许重跑的
// 回到待认领并可再次被认领。这是该开关 at-most-once 的那一半。
func TestIntegrationReapParksNoRerunRow(t *testing.T) {
	q, db := newIntegrationQueue(t, Config{
		Job:         "orders",
		Lease:       time.Minute,
		MaxAttempts: 3,
	})
	seed(t, db, q.tableName(), "orders", 2)

	tasks, err := q.Claim(context.Background(), "worker-a", 2)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("Claim = %v, %v", tasks, err)
	}
	if err := db.Exec("UPDATE `"+q.tableName()+
		"` SET rerun_allowed = 0 WHERE id = ?", tasks[0].ID).Error; err != nil {
		t.Fatalf("mark no-rerun: %v", err)
	}
	for _, task := range tasks {
		if err := db.Exec("UPDATE `"+q.tableName()+
			"` SET lease_until = DATE_SUB(NOW(3), INTERVAL 1 SECOND) WHERE id = ?", task.ID).Error; err != nil {
			t.Fatalf("expire lease: %v", err)
		}
	}

	reaped, dead, err := q.Reap(context.Background())
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if reaped != 1 || dead != 1 {
		t.Fatalf("Reap = %d/%d, want 1/1", reaped, dead)
	}
	if got := q.statusOf(t, tasks[0].ID); got != StatusDead {
		t.Fatalf("no-rerun row status = %d, want %d", got, StatusDead)
	}
	if got := q.reasonOf(t, tasks[0].ID); got != reasonNoRerun {
		t.Fatalf("no-rerun reason = %q, want %q", got, reasonNoRerun)
	}
	if !q.timeOf(t, tasks[0].ID, "fail_handle_time").Valid {
		t.Fatal("fail_handle_time not stamped on the parked row")
	}
	if got := q.statusOf(t, tasks[1].ID); got != StatusPending {
		t.Fatalf("rerun-allowed row status = %d, want %d", got, StatusPending)
	}

	again, err := q.Claim(context.Background(), "worker-b", 2)
	if err != nil {
		t.Fatalf("Claim after reap: %v", err)
	}
	if len(again) != 1 || again[0].ID != tasks[1].ID {
		t.Fatalf("Claim after reap = %+v, want only row %d", again, tasks[1].ID)
	}
	if again[0].Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", again[0].Attempts)
	}
}

func TestIntegrationReapDeadStampsFailTime(t *testing.T) {
	q, db := newIntegrationQueue(t, Config{
		Job:         "orders",
		Lease:       time.Minute,
		MaxAttempts: 1,
	})
	seed(t, db, q.tableName(), "orders", 1)

	tasks, err := q.Claim(context.Background(), "worker-a", 1)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Claim = %v, %v", tasks, err)
	}
	if !q.timeOf(t, tasks[0].ID, "start_handle_time").Valid {
		t.Fatal("start_handle_time not stamped on Claim")
	}
	// Expire the lease: with attempts already at the cap, Reap decides dead.
	// 让租约过期：attempts 已到上限，Reap 会判定 dead。
	if err := db.Exec("UPDATE `"+q.tableName()+
		"` SET lease_until = DATE_SUB(NOW(3), INTERVAL 1 SECOND) WHERE id = ?", tasks[0].ID).Error; err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	reaped, dead, err := q.Reap(context.Background())
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if reaped != 0 || dead != 1 {
		t.Fatalf("Reap = %d/%d, want 0/1", reaped, dead)
	}
	if got := q.statusOf(t, tasks[0].ID); got != StatusDead {
		t.Fatalf("status = %d, want %d (dead)", got, StatusDead)
	}
	if !q.timeOf(t, tasks[0].ID, "fail_handle_time").Valid {
		t.Fatal("fail_handle_time not stamped when Reap moves the row to dead")
	}
}

func TestIntegrationFailAfterLeaseLost(t *testing.T) {
	q, db := newIntegrationQueue(t, Config{Job: "orders", Lease: time.Minute})
	seed(t, db, q.tableName(), "orders", 1)

	tasks, err := q.Claim(context.Background(), "worker-a", 1)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Claim = %v, %v", tasks, err)
	}
	// Simulate a reap followed by another worker taking the row.
	// 模拟一次回收后另一个 worker 接管该行。
	if err := db.Exec("UPDATE `"+q.tableName()+
		"` SET owner = 'worker-b', attempts = 2 WHERE id = ?", tasks[0].ID).Error; err != nil {
		t.Fatalf("steal row: %v", err)
	}
	if _, err := q.Fail(context.Background(), tasks[0], errors.New("late")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Fail error = %v, want ErrLeaseLost", err)
	}
}

// TestIntegrationStaleClaimOfSameOwnerCannotWriteBack is the exact scenario the
// owner check cannot catch: the lease expires, the row is reaped, and the same
// worker instance claims it again. Owner is identical for both claims; attempts
// is not, so the first claim's completion must be rejected.
// TestIntegrationStaleClaimOfSameOwnerCannotWriteBack 正是 owner 检查抓不到的场景：
// 租约过期、行被回收，同一个 worker 实例再次认领。两次认领的 owner 完全相同，
// 只有 attempts 不同，因此第一次认领的收尾必须被拒绝。
func TestIntegrationStaleClaimOfSameOwnerCannotWriteBack(t *testing.T) {
	q, db := newIntegrationQueue(t, Config{
		Job:         "orders",
		Lease:       time.Minute,
		MaxAttempts: 5,
	})
	seed(t, db, q.tableName(), "orders", 1)

	first, err := q.Claim(context.Background(), "worker-a", 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first Claim = %v, %v", first, err)
	}
	if first[0].Attempts != 1 {
		t.Fatalf("first attempts = %d, want 1", first[0].Attempts)
	}

	// Reap semantics without waiting for the lease: the row goes back to
	// pending with its attempt counter intact.
	// 不等租约到期，直接按 Reap 的语义把行放回待处理态，attempts 保持不变。
	if err := db.Exec("UPDATE `"+q.tableName()+
		"` SET status = 0, owner = '', lease_until = NULL WHERE id = ?", first[0].ID).Error; err != nil {
		t.Fatalf("reap row: %v", err)
	}
	second, err := q.Claim(context.Background(), "worker-a", 1)
	if err != nil || len(second) != 1 {
		t.Fatalf("second Claim = %v, %v", second, err)
	}
	if second[0].Attempts != 2 {
		t.Fatalf("second attempts = %d, want 2", second[0].Attempts)
	}
	if second[0].Owner != first[0].Owner {
		t.Fatalf("owners differ (%q vs %q); the test must exercise the same owner",
			first[0].Owner, second[0].Owner)
	}

	if err := q.Succeed(context.Background(), first[0]); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale Succeed = %v, want ErrLeaseLost", err)
	}
	if _, err := q.Fail(context.Background(), first[0], errors.New("late")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale Fail = %v, want ErrLeaseLost", err)
	}
	if got := q.statusOf(t, first[0].ID); got != StatusClaimed {
		t.Fatalf("status after stale writes = %d, want %d (still claimed)", got, StatusClaimed)
	}
	if err := q.Succeed(context.Background(), second[0]); err != nil {
		t.Fatalf("current Succeed: %v", err)
	}
	if got := q.statusOf(t, second[0].ID); got != StatusDone {
		t.Fatalf("status = %d, want %d", got, StatusDone)
	}
}

func (q *Queue) tableName() string {
	return trimQuotes(q.table)
}

func (q *Queue) statusOf(t *testing.T, id uint64) int8 {
	t.Helper()
	var status int8
	if err := q.db.Raw("SELECT status FROM "+q.table+" WHERE id = ?", id).Scan(&status).Error; err != nil {
		t.Fatalf("status query: %v", err)
	}
	return status
}

func (q *Queue) reasonOf(t *testing.T, id uint64) string {
	t.Helper()
	var reason string
	if err := q.db.Raw("SELECT COALESCE(fail_reason, '') FROM "+q.table+" WHERE id = ?", id).Scan(&reason).Error; err != nil {
		t.Fatalf("fail_reason query: %v", err)
	}
	return reason
}

// timeOf reads one of the three handle-time columns. NULL (no such event yet)
// comes back as invalid — e.g. a row that succeeded without ever failing has no
// fail_handle_time, while every failure, retryable or dead, stamps it.
// timeOf 读取三个处理时间列之一。还没发生过对应事件时是 NULL（sql.NullTime.Valid=false）——
// 例如从未失败就成功的行没有 fail_handle_time；而每次失败，无论重试还是死亡，都会写它。
func (q *Queue) timeOf(t *testing.T, id uint64, column string) sql.NullTime {
	t.Helper()
	var got sql.NullTime
	if err := q.db.Raw("SELECT "+column+" FROM "+q.table+" WHERE id = ?", id).Scan(&got).Error; err != nil {
		t.Fatalf("%s query: %v", column, err)
	}
	return got
}

func trimQuotes(quoted string) string {
	out := make([]rune, 0, len(quoted))
	for _, r := range quoted {
		if r != '`' {
			out = append(out, r)
		}
	}
	return string(out)
}
