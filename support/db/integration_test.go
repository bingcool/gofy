package db_test

// Real-MySQL integration scenarios beyond the basic ping in db_test.go:
// transaction semantics, killed connections and pool recovery. Everything is
// gated on GOFY_TEST_MYSQL_DSN so plain `go test ./...` still passes without a
// server.
// db_test.go 基础连通性之外的真实 MySQL 场景：事务语义、被杀连接与连接池恢复。
// 全部由 GOFY_TEST_MYSQL_DSN 显式开启，没有服务时 go test ./... 照常通过。

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/bingcool/gofy/support/db"
	"go.uber.org/zap"
)

var (
	errRollbackNow     = errors.New("db integration: rollback now")
	errUnexpectedCount = errors.New("db integration: unexpected row count")
)

func integrationConn(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("GOFY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("GOFY_TEST_MYSQL_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, err := db.New(ctx, db.Config{
		DSN:          dsn,
		MaxOpenConns: 4,
		PingTimeout:  5 * time.Second,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(conn) })
	return conn
}

func TestIntegrationTransactionsCommitAndRollback(t *testing.T) {
	conn := integrationConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	g := conn.WithContext(ctx)

	table := "gofy_db_itx_" + uniqueSuffix()
	if err := g.Exec("CREATE TABLE " + table + " (id INT PRIMARY KEY, v VARCHAR(32))").Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _ = g.Exec("DROP TABLE IF EXISTS " + table) })

	count := func() int64 {
		var n int64
		if err := g.Raw("SELECT COUNT(*) FROM " + table).Scan(&n).Error; err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	// A committed transaction is durable.
	// 提交的事务是持久的。
	err := db.Transaction(ctx, conn, func(txCtx context.Context) error {
		return db.WithContext(txCtx, conn).Exec("INSERT INTO " + table + " (id, v) VALUES (1, 'committed')").Error
	})
	if err != nil {
		t.Fatalf("commit tx: %v", err)
	}
	if got := count(); got != 1 {
		t.Fatalf("rows after commit = %d, want 1", got)
	}

	// A rolled-back transaction leaves nothing behind, even though the INSERT
	// itself succeeded.
	// 回滚的事务不留痕迹，即使 INSERT 本身已经成功。
	err = db.Transaction(ctx, conn, func(txCtx context.Context) error {
		if err := db.WithContext(txCtx, conn).Exec("INSERT INTO " + table + " (id, v) VALUES (2, 'rolled-back')").Error; err != nil {
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

	// The pool must not hold a poisoned transaction after a rollback; the next
	// transaction commits normally.
	// 回滚后池里不能留下中毒的事务；下一个事务正常提交。
	if err := db.Transaction(ctx, conn, func(txCtx context.Context) error {
		return db.WithContext(txCtx, conn).Exec("INSERT INTO " + table + " (id, v) VALUES (3, 'after-rollback')").Error
	}); err != nil {
		t.Fatalf("tx after rollback: %v", err)
	}
	if got := count(); got != 2 {
		t.Fatalf("rows after follow-up commit = %d, want 2", got)
	}
}

func TestIntegrationKilledConnectionsFailFast(t *testing.T) {
	conn := integrationConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	g := conn.WithContext(ctx)

	// Kill every server-side session this pool opened. The next query must
	// surface a driver error instead of hanging until the ping timeout.
	// 杀掉这个池打开的所有服务端会话。下一个查询必须立刻返回驱动错误，
	// 而不是挂到 ping 超时。
	if killed := killPoolSessions(t, g); killed == 0 {
		t.Fatal("no sessions to kill — pool had no live connections")
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		var one int
		err := g.Raw("SELECT 1").Scan(&one).Error
		if err == nil {
			break // the pool dialed a fresh connection; recovery is the next test
		}
		if time.Now().After(deadline) {
			t.Fatalf("killed connection never returned a driver error: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestIntegrationPoolRecoversAfterKill(t *testing.T) {
	conn := integrationConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	g := conn.WithContext(ctx)

	killPoolSessions(t, g)

	// New queries transparently dial new connections; a burst of concurrent
	// readers proves the pool is healthy again rather than serving one lucky
	// reconnect.
	// 后续查询透明地新建连接；并发读批次证明池整体已恢复，而不是靠一次幸运重连。
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var one int
			qCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			errs[i] = conn.WithContext(qCtx).Raw("SELECT 1").Scan(&one).Error
			if errs[i] == nil && one != 1 {
				errs[i] = errUnexpectedCount
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("query %d after kill: %v", i, err)
		}
	}
}

// killPoolSessions warms the pool, then kills every server-side session of the
// DSN's user on this host. Returns the number of sessions killed; 0 means
// nothing was connected, which the callers treat as a broken premise.
// killPoolSessions 先预热池，再杀掉该 DSN 用户在本机的所有服务端会话。返回杀掉
// 的会话数；0 表示没有连接存活，调用方视为前提被破坏。
func killPoolSessions(t *testing.T, g *gorm.DB) int {
	t.Helper()
	var one int
	if err := g.Raw("SELECT 1").Scan(&one).Error; err != nil {
		t.Fatalf("warm query: %v", err)
	}

	// CURRENT_USER() looks like `user@host` (sometimes quoted); the
	// processlist splits it back into two columns.
	// CURRENT_USER() 形如 `user@host`（可能带引号）；processlist 把它拆回两列。
	var current string
	if err := g.Raw("SELECT CURRENT_USER()").Scan(&current).Error; err != nil {
		t.Fatalf("current user: %v", err)
	}
	current = strings.Trim(current, "'")
	// Filter by user only: the host column in processlist holds the client
	// address (ip:port), which never matches CURRENT_USER()'s user@pattern
	// host.
	// 只按 user 过滤：processlist 的 Host 列是客户端地址（ip:port），
	// 与 CURRENT_USER() 的 user@pattern 主机部分永远对不上。
	user := current
	if i := strings.LastIndexByte(current, '@'); i >= 0 {
		user = current[:i]
	}

	rows, err := g.Raw(
		"SELECT Id FROM information_schema.processlist WHERE User = ? AND Id <> CONNECTION_ID()",
		user,
	).Rows()
	if err != nil {
		t.Fatalf("processlist: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []uint64
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	killed := 0
	for _, id := range ids {
		// KILL takes a literal connection id, not a bind parameter, on some
		// MySQL builds; interpolate a scanned integer instead of a string.
		// 部分 MySQL 版本的 KILL 不接受绑定参数；这里内插的是从 processlist
		// 读出的整数，不是外部输入。
		if err := g.Exec("KILL " + strconv.FormatUint(id, 10)).Error; err == nil {
			killed++
		}
	}
	return killed
}

func uniqueSuffix() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}
