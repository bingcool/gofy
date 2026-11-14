package claim

import (
	"context"
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
		stmt := "INSERT INTO `" + table + "` (job, status, payload) VALUES " + strings.Join(rows, ",")
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
	if err := db.Raw("SELECT COUNT(*) FROM `" + q.tableName() + "` WHERE job = 'other' AND status = 0").
		Scan(&untouched).Error; err != nil {
		t.Fatalf("count other: %v", err)
	}
	if untouched != 5 {
		t.Fatalf("other job rows = %d, want 5 untouched", untouched)
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

	if err := q.Succeed(context.Background(), Task{ID: task.ID, Owner: "worker-b"}); !errors.Is(err, ErrLeaseLost) {
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
	if err := q.Fail(context.Background(), tasks[0], errors.New("boom")); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	// The retry delay has not elapsed, so the row must stay invisible.
	// 重试延迟未到，这一行必须仍不可见。
	if next, err := q.Claim(context.Background(), "worker-a", 1); err != nil || len(next) != 0 {
		t.Fatalf("immediate Claim = %v, %v; want none", next, err)
	}

	time.Sleep(300 * time.Millisecond)
	tasks, err = q.Claim(context.Background(), "worker-b", 1)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("delayed Claim = %v, %v", tasks, err)
	}
	if tasks[0].Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", tasks[0].Attempts)
	}

	if err := q.Fail(context.Background(), tasks[0], errors.New("boom again")); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if got := q.statusOf(t, tasks[0].ID); got != StatusDead {
		t.Fatalf("status = %d, want %d (dead)", got, StatusDead)
	}

	reaped, dead, err := q.Reap(context.Background())
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if reaped != 0 || dead != 0 {
		t.Fatalf("Reap = %d/%d, want 0/0 for a dead row", reaped, dead)
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
	if err := q.Fail(context.Background(), tasks[0], errors.New("late")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Fail error = %v, want ErrLeaseLost", err)
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

func trimQuotes(quoted string) string {
	out := make([]rune, 0, len(quoted))
	for _, r := range quoted {
		if r != '`' {
			out = append(out, r)
		}
	}
	return string(out)
}
