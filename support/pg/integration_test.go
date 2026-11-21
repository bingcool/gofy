package pg_test

// Real-PostgreSQL integration scenarios beyond the basic ping in pg_test.go:
// transaction semantics and pool recovery after server-side termination.
// Gated on GOFY_TEST_PG_DSN; without it `go test ./...` skips everything here.
// pg_test.go 基础连通性之外的真实 PostgreSQL 场景：事务语义与服务端终止后的
// 连接池恢复。由 GOFY_TEST_PG_DSN 显式开启，未设置时全部跳过。

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/bingcool/gofy/support/db"
	"github.com/bingcool/gofy/support/pg"
	"go.uber.org/zap"
)

var (
	errRollbackNow = errors.New("pg integration: rollback now")
	errWaitTimeout = errors.New("pg integration: recovery wait timed out")
)

func integrationConn(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("GOFY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("GOFY_TEST_PG_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, err := pg.New(ctx, pg.Config{
		DSN:          dsn,
		MaxOpenConns: 4,
		PingTimeout:  5 * time.Second,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close(conn) })
	return conn
}

func TestIntegrationTransactionsCommitAndRollback(t *testing.T) {
	conn := integrationConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	g := conn.WithContext(ctx)

	table := "gofy_pg_itx_" + uniqueSuffix()
	if err := g.Exec(`CREATE TABLE ` + table + ` (id INT PRIMARY KEY, v TEXT)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _ = g.Exec(`DROP TABLE IF EXISTS ` + table) })

	count := func() int64 {
		var n int64
		if err := g.Raw(`SELECT COUNT(*) FROM ` + table).Scan(&n).Error; err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	// A committed transaction is durable.
	// 提交的事务是持久的。
	if err := db.Transaction(ctx, conn, func(txCtx context.Context) error {
		return db.WithContext(txCtx, conn).Exec(
			`INSERT INTO ` + table + ` (id, v) VALUES (1, 'committed')`).Error
	}); err != nil {
		t.Fatalf("commit tx: %v", err)
	}
	if got := count(); got != 1 {
		t.Fatalf("rows after commit = %d, want 1", got)
	}

	// A rolled-back transaction leaves nothing behind even though the INSERT
	// itself succeeded.
	// 回滚的事务不留痕迹，即使 INSERT 本身已经成功。
	err := db.Transaction(ctx, conn, func(txCtx context.Context) error {
		if err := db.WithContext(txCtx, conn).Exec(
			`INSERT INTO ` + table + ` (id, v) VALUES (2, 'rolled-back')`).Error; err != nil {
			return err
		}
		return errRollbackNow
	})
	if !errors.Is(err, errRollbackNow) {
		t.Fatalf("Transaction must surface the handler error, got %v", err)
	}
	if got := count(); got != 1 {
		t.Fatalf("rows after rollback = %d, want 1 (rollback leaked a row)", got)
	}

	// The pool must not hold a poisoned transaction after a rollback.
	// 回滚后池里不能留下中毒的事务。
	if err := db.Transaction(ctx, conn, func(txCtx context.Context) error {
		return db.WithContext(txCtx, conn).Exec(
			`INSERT INTO ` + table + ` (id, v) VALUES (3, 'after-rollback')`).Error
	}); err != nil {
		t.Fatalf("tx after rollback: %v", err)
	}
	if got := count(); got != 2 {
		t.Fatalf("rows after follow-up commit = %d, want 2", got)
	}
}

func TestIntegrationPoolRecoversAfterTermination(t *testing.T) {
	conn := integrationConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	g := conn.WithContext(ctx)

	terminated := terminatePoolSessions(t, g)
	if terminated == 0 {
		t.Fatal("no sessions to terminate — pool had no live connections")
	}

	// Every subsequent query must transparently redial: a burst of concurrent
	// readers proves the pool recovered as a whole.
	// 后续查询必须透明地重连：并发读批次证明池整体恢复。
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var one int
			qCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			errs[i] = conn.WithContext(qCtx).Raw(`SELECT 1`).Scan(&one).Error
			if errs[i] == nil && one != 1 {
				errs[i] = errWaitTimeout
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("query %d after termination: %v", i, err)
		}
	}
}

// terminatePoolSessions warms the pool, then asks the server to terminate
// every session of the current role except this one. Returns the number of
// terminated sessions; 0 means nothing was connected.
// terminatePoolSessions 先预热池，再让服务端终止当前角色的其他会话（保留本
// 连接）。返回终止的会话数；0 表示没有连接存活。
func terminatePoolSessions(t *testing.T, g *gorm.DB) int {
	t.Helper()
	var one int
	if err := g.Raw(`SELECT 1`).Scan(&one).Error; err != nil {
		t.Fatalf("warm query: %v", err)
	}

	// usename and application_name identify our sessions; pg_backend_pid()
	// excludes the connection issuing the KILL-equivalent.
	// 用 usename 识别我们的会话；pg_backend_pid() 排除执行本语句的连接。
	var usename string
	if err := g.Raw(`SELECT usename FROM pg_stat_activity WHERE pid = pg_backend_pid()`).Scan(&usename).Error; err != nil {
		t.Fatalf("current role: %v", err)
	}

	var pids []int
	if err := g.Raw(
		`SELECT pid FROM pg_stat_activity WHERE usename = ? AND pid <> pg_backend_pid() AND state IS NOT NULL`,
		usename,
	).Scan(&pids).Error; err != nil {
		t.Fatalf("stat_activity: %v", err)
	}

	terminated := 0
	for _, pid := range pids {
		// pg_terminate_backend takes a bigint; the pid comes from the server's
		// own view of our sessions, not from user input.
		// pg_terminate_backend 接收 bigint；pid 来自服务端对我们会话的记录，
		// 不是用户输入。
		if err := g.Exec(`SELECT pg_terminate_backend(` + strconv.Itoa(pid) + `)`).Error; err == nil {
			terminated++
		}
	}
	return terminated
}

func uniqueSuffix() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}
