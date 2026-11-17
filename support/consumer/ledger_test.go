package consumer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/bingcool/gofy/core"
)

// newLedgerProcessor builds the shared chain on an in-memory SQLite database.
// One connection is enough: the ledger's correctness comes from its CAS
// statements, not from connection-level parallelism, and SQLite locks a single
// writer anyway.
// newLedgerProcessor 在内存 SQLite 上构造共用链路。一条连接就够：台账的正确性来自
// 它的 CAS 语句而不是连接级并发，而 SQLite 本来也只允许一个写者。
func newLedgerProcessor(t *testing.T, handler HandlerFunc, maxAttempts int, lease time.Duration) (*Processor, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:ledger_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Create the table the way a deployment does — from a migration script, not
	// from AutoMigrate. The declared "datetime" type is what makes the SQLite
	// driver scan the timestamp columns back into time.Time; the production
	// MySQL DDL uses datetime(3), and the ledger only compares those values, so
	// the difference stays inside the test.
	// 按部署的方式建表：来自迁移脚本而不是 AutoMigrate。声明成 "datetime" 是让 SQLite
	// 驱动把时间列扫描回 time.Time 的关键；生产 MySQL 的 DDL 用 datetime(3)，台账只
	// 比较这些值，因此这点差异只存在于测试里。
	if err := db.Exec(`CREATE TABLE worker_consumed_messages (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  consumer    TEXT    NOT NULL,
  message_id  TEXT    NOT NULL,
  status      TEXT    NOT NULL,
  attempts    INTEGER NOT NULL,
  last_error  TEXT,
  created_at  datetime,
  updated_at  datetime
)`).Error; err != nil {
		t.Fatalf("create ledger table: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX uk_worker_consumed ON worker_consumed_messages (consumer, message_id)`).Error; err != nil {
		t.Fatalf("create ledger index: %v", err)
	}

	rt := core.NewRuntime(core.RuntimeConfig{Logger: zap.NewNop(), DB: db})
	proc, err := NewProcessor(rt, ProcessorConfig{
		Consumer:        testConsumerName,
		Handler:         handler,
		Idempotency:     true,
		MaxAttempts:     maxAttempts,
		ProcessingLease: lease,
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	return proc, db
}

func ledgerMessage(id string) Message {
	return Message{
		ID:     id,
		Source: "kafka",
		Topic:  "orders",
		Body:   []byte(`{"type":"order.paid","payload":{"order_id":1}}`),
	}
}

func ledgerRow(t *testing.T, db *gorm.DB, messageID string) consumedMessage {
	t.Helper()
	var row consumedMessage
	if err := db.Where("consumer = ? AND message_id = ?", testConsumerName, messageID).
		First(&row).Error; err != nil {
		t.Fatalf("read ledger row: %v", err)
	}
	return row
}

// TestLedgerRunsOnlyOneHandlerPerMessage is acceptance test 1 and 2 of the
// audit: twenty concurrent deliveries of the same message, one handler. The
// deliveries that arrive while the handler runs must return ErrInFlight
// ("redeliver later"), not nil — nil would acknowledge the copy while the first
// handler still runs. Deliveries that arrive after it completed are skipped as
// duplicates and acknowledged, which is why the split between nil and
// ErrInFlight is timing dependent while the handler count is not.
// TestLedgerRunsOnlyOneHandlerPerMessage 对应审计的验收 1、2：同一消息并发投递 20 次，
// 只有一个 handler 进入。handler 运行期间到达的投递必须返回 ErrInFlight（稍后重投），
// 而不是 nil——nil 会在第一个 handler 还在跑时把副本确认掉。处理完成之后才到达的投递
// 按重复跳过并确认，因此 nil 与 ErrInFlight 的数量取决于时序，而 handler 次数不是。
func TestLedgerRunsOnlyOneHandlerPerMessage(t *testing.T) {
	var entered atomic.Int32
	release := make(chan struct{})
	proc, _ := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		entered.Add(1)
		<-release
		return nil
	}, 5, time.Minute)

	const deliveries = 20
	errs := make(chan error, deliveries)
	var wg sync.WaitGroup
	for i := 0; i < deliveries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- proc.Handle(context.Background(), ledgerMessage("m-concurrent"))
		}()
	}

	deadline := time.Now().Add(5 * time.Second)
	for entered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := entered.Load(); got != 1 {
		t.Fatalf("handlers entered before release = %d, want 1", got)
	}
	close(release)
	wg.Wait()
	close(errs)

	acknowledged := 0
	for err := range errs {
		switch {
		case err == nil:
			acknowledged++
		case errors.Is(err, ErrInFlight):
		default:
			t.Fatalf("unexpected Handle error: %v", err)
		}
	}
	if acknowledged == 0 {
		t.Fatal("every delivery was asked to redeliver; the completed message was never acknowledged")
	}
	if got := entered.Load(); got != 1 {
		t.Fatalf("handler calls = %d, want 1", got)
	}
}

// TestLedgerRefusesToAcknowledgeAnInFlightMessage states the rule on its own:
// a duplicate that arrives while the first handler holds a valid lease must be
// retryable, otherwise the only remaining copy is gone if that handler dies.
// TestLedgerRefusesToAcknowledgeAnInFlightMessage 单独固定这条规则：第一个 handler
// 持有有效租约时到达的重复消息必须是可重试的，否则一旦它崩溃，仅剩的副本就没了。
func TestLedgerRefusesToAcknowledgeAnInFlightMessage(t *testing.T) {
	var entered atomic.Int32
	release := make(chan struct{})
	proc, db := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		entered.Add(1)
		<-release
		return nil
	}, 5, time.Minute)

	first := make(chan error, 1)
	go func() { first <- proc.Handle(context.Background(), ledgerMessage("m-inflight")) }()
	for entered.Load() == 0 {
		time.Sleep(2 * time.Millisecond)
	}

	err := proc.Handle(context.Background(), ledgerMessage("m-inflight"))
	if !errors.Is(err, ErrInFlight) {
		t.Fatalf("duplicate Handle = %v, want ErrInFlight", err)
	}
	if row := ledgerRow(t, db, "m-inflight"); row.Status != consumedStatusProcessing || row.Attempts != 1 {
		t.Fatalf("ledger row = %s/attempts %d, want processing/1", row.Status, row.Attempts)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if row := ledgerRow(t, db, "m-inflight"); row.Status != consumedStatusDone {
		t.Fatalf("status after completion = %s, want done", row.Status)
	}
}

// TestLedgerTakesOverExpiredProcessing covers acceptance test 3: once the lease
// has expired the message may be handled again, with a new attempt value. The
// old attempt can no longer write its result.
// TestLedgerTakesOverExpiredProcessing 对应验收 3：租约过期后消息可以被重新处理，
// 并拿到新的 attempt；旧的 attempt 再也写不进结果。
func TestLedgerTakesOverExpiredProcessing(t *testing.T) {
	var calls atomic.Int32
	proc, db := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		calls.Add(1)
		return nil
	}, 5, time.Minute)

	stale := consumedMessage{
		Consumer:  testConsumerName,
		MessageID: "m-expired",
		Status:    consumedStatusProcessing,
		Attempts:  1,
		CreatedAt: time.Now().Add(-2 * time.Hour),
		UpdatedAt: time.Now().Add(-2 * time.Hour),
	}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatalf("seed processing row: %v", err)
	}

	if err := proc.Handle(context.Background(), ledgerMessage("m-expired")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler calls = %d, want 1", got)
	}
	row := ledgerRow(t, db, "m-expired")
	if row.Status != consumedStatusDone || row.Attempts != 2 {
		t.Fatalf("row = %s/attempts %d, want done/2", row.Status, row.Attempts)
	}
	// The implicit lease is updated_at: a takeover must move it forward,
	// otherwise the next redelivery would treat the row as expired again.
	// 隐式租约就是 updated_at：接管必须把它推进，否则下一次重投仍会认为它过期。
	if age := time.Since(row.UpdatedAt); age > time.Minute {
		t.Fatalf("updated_at not advanced by the takeover: age %v", age)
	}

	if err := proc.deduper.succeed(context.Background(), "m-expired", 1); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("stale succeed = %v, want ErrStaleClaim", err)
	}
}

// TestLedgerTerminalStatesAreFinal covers acceptance test 4: dead and done rows
// are skipped and acknowledged, and no late write may reopen them.
// TestLedgerTerminalStatesAreFinal 对应验收 4：dead 与 done 的行直接跳过并确认，任何
// 迟到写入都不能把它们重新打开。
func TestLedgerTerminalStatesAreFinal(t *testing.T) {
	var calls atomic.Int32
	proc, db := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		calls.Add(1)
		return nil
	}, 5, time.Minute)

	now := time.Now()
	for _, row := range []consumedMessage{
		{Consumer: testConsumerName, MessageID: "m-done", Status: consumedStatusDone, Attempts: 1, CreatedAt: now, UpdatedAt: now},
		{Consumer: testConsumerName, MessageID: "m-dead", Status: consumedStatusDead, Attempts: 5, CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed %s: %v", row.MessageID, err)
		}
	}

	for _, id := range []string{"m-done", "m-dead"} {
		if err := proc.Handle(context.Background(), ledgerMessage(id)); err != nil {
			t.Fatalf("Handle(%s) = %v, want acknowledgement", id, err)
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("handlers ran %d times for terminal rows, want 0", got)
	}

	// A late success/failure from an older attempt must not rewrite them.
	if err := proc.deduper.fail(context.Background(), "m-done", 1, errors.New("late")); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("fail on done = %v, want ErrStaleClaim", err)
	}
	if err := proc.deduper.park(context.Background(), "m-done", 1, errors.New("late")); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("park on done = %v, want ErrStaleClaim", err)
	}
	if err := proc.deduper.park(context.Background(), "m-dead", 5, errors.New("late")); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("park on dead = %v, want ErrStaleClaim", err)
	}
	if row := ledgerRow(t, db, "m-done"); row.Status != consumedStatusDone {
		t.Fatalf("done row became %s", row.Status)
	}
	if row := ledgerRow(t, db, "m-dead"); row.Status != consumedStatusDead {
		t.Fatalf("dead row became %s", row.Status)
	}
}

// TestLedgerRetriesFailedMessage covers the retry edge: only a recorded failure
// may be picked up again, and it gets a new attempt value.
// TestLedgerRetriesFailedMessage 覆盖重试边界：只有记录为 failed 的消息可以被再次
// 拾取，并且会拿到新的 attempt。
func TestLedgerRetriesFailedMessage(t *testing.T) {
	var calls atomic.Int32
	proc, db := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		calls.Add(1)
		return nil
	}, 5, time.Minute)

	now := time.Now()
	if err := db.Create(&consumedMessage{
		Consumer: testConsumerName, MessageID: "m-failed",
		Status: consumedStatusFailed, Attempts: 1,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed failed row: %v", err)
	}

	if err := proc.Handle(context.Background(), ledgerMessage("m-failed")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler calls = %d, want 1", got)
	}
	row := ledgerRow(t, db, "m-failed")
	if row.Status != consumedStatusDone || row.Attempts != 2 {
		t.Fatalf("row = %s/attempts %d, want done/2", row.Status, row.Attempts)
	}
}

// TestLedgerExhaustedAttemptsBecomeDead covers acceptance test 5 from the other
// side: once the budget is gone the row is parked as dead exactly once and no
// handler runs again.
// TestLedgerExhaustedAttemptsBecomeDead 从另一侧覆盖验收 5：预算用尽后该行只被标记
// 一次 dead，handler 不再执行。
func TestLedgerExhaustedAttemptsBecomeDead(t *testing.T) {
	var calls atomic.Int32
	proc, db := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		calls.Add(1)
		return nil
	}, 2, time.Minute)

	now := time.Now()
	if err := db.Create(&consumedMessage{
		Consumer: testConsumerName, MessageID: "m-exhausted",
		Status: consumedStatusFailed, Attempts: 2,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed failed row: %v", err)
	}

	if err := proc.Handle(context.Background(), ledgerMessage("m-exhausted")); err != nil {
		t.Fatalf("Handle = %v, want acknowledgement", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("handler calls = %d, want 0", got)
	}
	row := ledgerRow(t, db, "m-exhausted")
	if row.Status != consumedStatusDead || row.Attempts != 2 {
		t.Fatalf("row = %s/attempts %d, want dead/2", row.Status, row.Attempts)
	}
	if row.LastError != "attempts exhausted" {
		t.Fatalf("last_error = %q, want the exhaustion marker", row.LastError)
	}
}

// TestLedgerRecordsRetryableFailure makes sure a handler error is written by the
// attempt that owns the row, so the next delivery sees the reason and a new
// attempt number.
// TestLedgerRecordsRetryableFailure 确认 handler 的错误由持有该行的 attempt 写入，
// 下一次投递因此能看到原因并拿到新的 attempt。
func TestLedgerRecordsRetryableFailure(t *testing.T) {
	proc, db := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		return errors.New("downstream unavailable")
	}, 5, time.Minute)

	err := proc.Handle(context.Background(), ledgerMessage("m-fail"))
	if err == nil || errors.Is(err, ErrInFlight) {
		t.Fatalf("Handle = %v, want the handler's retryable error", err)
	}
	row := ledgerRow(t, db, "m-fail")
	if row.Status != consumedStatusFailed || row.Attempts != 1 {
		t.Fatalf("row = %s/attempts %d, want failed/1", row.Status, row.Attempts)
	}
	if row.LastError != "downstream unavailable" {
		t.Fatalf("last_error = %q", row.LastError)
	}
}

// failPark installs a trigger that rejects every UPDATE on the ledger, which is
// how these tests simulate "the claim succeeded, then the store went away".
// Nothing else in the chain writes with UPDATE before the outcome, so the
// trigger isolates the park path.
// failPark 装一个拒绝台账 UPDATE 的触发器，用来模拟"认领成功、随后存储不可用"。
// 链路在写结果之前没有别的 UPDATE，因此触发器正好隔离出 park 这条路径。
func failPark(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TRIGGER ledger_down BEFORE UPDATE ON worker_consumed_messages
BEGIN
  SELECT RAISE(ABORT, 'ledger unavailable');
END`).Error; err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
}

// TestLedgerRetriesWhenParkFails covers P1-03: a permanent failure whose dead
// state cannot be written must not be acknowledged. Returning nil here would
// delete the broker's copy of a message that never succeeded and leave no row
// to investigate.
// TestLedgerRetriesWhenParkFails 覆盖 P1-03：永久失败的 dead 状态写不进去时不得确认。
// 这里返回 nil 会删掉一条从未成功的消息在 broker 上的副本，台账里也没有可排查的行。
func TestLedgerRetriesWhenParkFails(t *testing.T) {
	proc, db := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		return Permanent(errors.New("unsupported event type"))
	}, 5, time.Minute)
	failPark(t, db)

	err := proc.Handle(context.Background(), ledgerMessage("m-park-fail"))
	if err == nil {
		t.Fatal("Handle acknowledged a permanent failure whose dead state was never recorded")
	}
	if IsPermanent(err) {
		t.Fatalf("error = %v, want a retryable error; a permanent one would let an adapter discard it", err)
	}
}

// TestLedgerRetriesUndecodableMessageWhenParkFails is the same rule on the
// decode path: a body that cannot be decoded is only safe to acknowledge once
// its dead state is durable.
// TestLedgerRetriesUndecodableMessageWhenParkFails 是同一条规则在解码路径上的体现：
// 只有 dead 状态落库后，无法解码的消息才可以确认。
func TestLedgerRetriesUndecodableMessageWhenParkFails(t *testing.T) {
	proc, db := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		t.Fatal("handler must not run for an undecodable envelope")
		return nil
	}, 5, time.Minute)
	failPark(t, db)

	msg := ledgerMessage("m-decode-fail")
	msg.Body = []byte("not-json")
	if err := proc.Handle(context.Background(), msg); err == nil {
		t.Fatal("Handle acknowledged an undecodable message whose dead state was never recorded")
	}
}

// TestLedgerAcknowledgesPermanentFailureWhenParkSucceeds keeps the other half
// of the contract pinned: once the dead state is durable the message is
// acknowledged, so a poison message does not come back forever.
// TestLedgerAcknowledgesPermanentFailureWhenParkSucceeds 固定契约的另一半：dead 状态
// 落库后消息就被确认，毒消息因此不会无限重投。
func TestLedgerAcknowledgesPermanentFailureWhenParkSucceeds(t *testing.T) {
	proc, db := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		return Permanent(errors.New("unsupported event type"))
	}, 5, time.Minute)

	if err := proc.Handle(context.Background(), ledgerMessage("m-park-ok")); err != nil {
		t.Fatalf("Handle = %v, want nil after the dead state was recorded", err)
	}
	if row := ledgerRow(t, db, "m-park-ok"); row.Status != consumedStatusDead {
		t.Fatalf("status = %s, want dead", row.Status)
	}
}

// TestLedgerLeaseIsWrittenInDatabaseTime pins the write half of P2-05. The
// SQLite driver stores a Go time.Time together with its numeric offset, while
// CURRENT_TIMESTAMP is UTC without one: a value without an offset can only have
// come from the database. That is the whole point — the lease must not carry the
// writing node's clock into every other node's decision.
// TestLedgerLeaseIsWrittenInDatabaseTime 固定 P2-05 的写入侧。SQLite 驱动会把 Go 的
// time.Time 连同时区偏移一起存下来，而 CURRENT_TIMESTAMP 是不带偏移的 UTC：没有偏移的值
// 只可能来自数据库。这正是重点——租约不能把写入节点的时钟带到其他所有节点的判断里。
func TestLedgerLeaseIsWrittenInDatabaseTime(t *testing.T) {
	proc, db := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		return nil
	}, 5, time.Minute)

	if _, err := proc.deduper.claim(context.Background(), "m-write-clock"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	var stored string
	if err := db.Raw(
		`SELECT CAST(updated_at AS TEXT) FROM worker_consumed_messages WHERE consumer = ? AND message_id = ?`,
		testConsumerName, "m-write-clock",
	).Scan(&stored).Error; err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if strings.ContainsAny(stored, "+T") {
		t.Fatalf("updated_at = %q looks like a Go timestamp; the lease must be written by the database clock", stored)
	}
}

// TestLedgerLeaseDecisionComesFromTheDatabase pins the read half: the expiry
// question is answered by the row stored in the database, not by whatever
// timestamp the caller happens to hold. A struct with no UpdatedAt used to mean
// "expired, take over"; now it means nothing at all.
// TestLedgerLeaseDecisionComesFromTheDatabase 固定读取侧：过期与否由数据库里的那一行回答，
// 不由调用方手里的时间戳回答。过去 struct 里没有 UpdatedAt 就等于"已过期，接管"；现在它
// 什么都不代表。
func TestLedgerLeaseDecisionComesFromTheDatabase(t *testing.T) {
	proc, db := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		return nil
	}, 5, time.Minute)

	if _, err := proc.deduper.claim(context.Background(), "m-read-clock"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	var id uint64
	if err := db.Raw(
		`SELECT id FROM worker_consumed_messages WHERE consumer = ? AND message_id = ?`,
		testConsumerName, "m-read-clock",
	).Scan(&id).Error; err != nil {
		t.Fatalf("read id: %v", err)
	}

	expired, err := proc.deduper.leaseExpired(context.Background(), consumedMessage{ID: id})
	if err != nil {
		t.Fatalf("leaseExpired: %v", err)
	}
	if expired {
		t.Fatal("a row written moments ago was judged expired because the caller's struct carried no timestamp")
	}

	// Backdate it past the lease: now the database says expired, and it is still
	// the database saying it.
	// 把时间戳挪到租约之外：此时数据库说已过期，而说这话的仍然是数据库。
	if err := db.Exec(
		`UPDATE worker_consumed_messages SET updated_at = datetime('now', '-3600 seconds') WHERE id = ?`, id,
	).Error; err != nil {
		t.Fatalf("backdate: %v", err)
	}
	expired, err = proc.deduper.leaseExpired(context.Background(), consumedMessage{ID: id})
	if err != nil {
		t.Fatalf("leaseExpired after backdate: %v", err)
	}
	if !expired {
		t.Fatal("a row backdated an hour was still treated as under a live lease")
	}
}

// TestLedgerSurfacesFailedDoneMarker pins B2: a done marker that cannot be
// stored must not block the acknowledgement — the side effect already happened
// — but it must reach OnLedgerError, because that hook is the only signal that
// the ledger now disagrees with reality.
// TestLedgerSurfacesFailedDoneMarker 固定 B2：写不进去的 done 标记不能挡住确认——副作用
// 已经发生——但必须送到 OnLedgerError，因为这个钩子是"台账已与现实不符"的唯一信号。
func TestLedgerSurfacesFailedDoneMarker(t *testing.T) {
	var db *gorm.DB
	proc, opened := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		// Renaming the table makes the succeed UPDATE fail after the handler has
		// already produced its side effect.
		// 重命名表让 succeed 的 UPDATE 在 handler 已产生副作用之后失败。
		return db.Exec("ALTER TABLE worker_consumed_messages RENAME TO worker_consumed_messages_hidden").Error
	}, 5, time.Minute)
	db = opened

	var calls atomic.Int32
	proc.onLedgerError = func(messageID string, err error) {
		calls.Add(1)
		if messageID != "m-marker" {
			t.Errorf("hook messageID = %q, want m-marker", messageID)
		}
		if err == nil {
			t.Error("hook received a nil error")
		}
	}

	if err := proc.Handle(context.Background(), ledgerMessage("m-marker")); err != nil {
		t.Fatalf("Handle = %v, want nil: a failed marker must not cancel the acknowledgement", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("OnLedgerError calls = %d, want 1", got)
	}
}

// TestRunHandlerRenewsLeaseWhileRunning pins B8: a handler that outlives the
// processing lease keeps the row locked, so the copy the broker redelivers is
// held back instead of taking the work over mid-flight. The test backdates the
// lease, then waits for the renewer to move it forward again.
// TestRunHandlerRenewsLeaseWhileRunning 固定 B8：活过 processing 租约的 handler 仍持有该行，
// broker 重投的副本因此被挡在外面，而不是在半途接管这笔活。测试先把租约拨旧，再等续期者把它
// 重新推近。
func TestRunHandlerRenewsLeaseWhileRunning(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	proc, db := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		close(entered)
		<-release
		return nil
	}, 5, 600*time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- proc.Handle(context.Background(), ledgerMessage("m-renew")) }()
	<-entered

	if err := db.Exec(
		`UPDATE worker_consumed_messages SET updated_at = datetime('now', '-3600 seconds') WHERE consumer = ? AND message_id = ?`,
		testConsumerName, "m-renew",
	).Error; err != nil {
		t.Fatalf("backdate lease: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var recent int64
		if err := db.Raw(
			`SELECT COUNT(*) FROM worker_consumed_messages WHERE consumer = ? AND message_id = ? AND updated_at > datetime('now', '-60 seconds')`,
			testConsumerName, "m-renew",
		).Scan(&recent).Error; err != nil {
			t.Fatalf("read lease: %v", err)
		}
		if recent == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the processing lease was not renewed while the handler ran")
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

// TestDeduperRenewFencesStaleAttempt pins the fencing half of the renewal: only
// the claim that currently owns the row may extend it.
// TestDeduperRenewFencesStaleAttempt 固定续期的围栏那一半：只有当前持有该行的认领才能续期。
func TestDeduperRenewFencesStaleAttempt(t *testing.T) {
	proc, _ := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		return nil
	}, 5, time.Minute)

	ctx := context.Background()
	if _, err := proc.deduper.claim(ctx, "m-renew-cas"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := proc.deduper.renew(ctx, "m-renew-cas", 1); err != nil {
		t.Fatalf("renew as the owner: %v", err)
	}
	if err := proc.deduper.renew(ctx, "m-renew-cas", 2); !errors.Is(err, ErrStaleClaim) {
		t.Fatalf("renew as a stale attempt = %v, want ErrStaleClaim", err)
	}
}

// TestLedgerPruneRemovesOnlyOldTerminalRows pins B9: the ledger gains a way to
// shed terminal history — done and dead rows past the window — without touching
// rows that are still live, still retryable, or belong to another consumer.
// TestLedgerPruneRemovesOnlyOldTerminalRows 固定 B9：台账有了丢弃终态历史的手段——窗口之外的
// done 与 dead 行——同时不动仍活着、仍可重试、或属于别的消费者的行。
func TestLedgerPruneRemovesOnlyOldTerminalRows(t *testing.T) {
	proc, db := newLedgerProcessor(t, func(context.Context, Message, Envelope) error {
		return nil
	}, 5, time.Minute)

	old := time.Now().Add(-48 * time.Hour)
	rows := []consumedMessage{
		{Consumer: testConsumerName, MessageID: "m-old-done", Status: consumedStatusDone, Attempts: 1, CreatedAt: old, UpdatedAt: old},
		{Consumer: testConsumerName, MessageID: "m-old-dead", Status: consumedStatusDead, Attempts: 5, CreatedAt: old, UpdatedAt: old},
		{Consumer: testConsumerName, MessageID: "m-recent-done", Status: consumedStatusDone, Attempts: 1},
		{Consumer: testConsumerName, MessageID: "m-old-failed", Status: consumedStatusFailed, Attempts: 2, CreatedAt: old, UpdatedAt: old},
		{Consumer: "other-consumer", MessageID: "m-foreign-done", Status: consumedStatusDone, Attempts: 1, CreatedAt: old, UpdatedAt: old},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", rows[i].MessageID, err)
		}
	}

	deleted, err := proc.Prune(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("Prune deleted %d rows, want 2 (old done and old dead)", deleted)
	}

	var remaining []string
	if err := db.Model(&consumedMessage{}).Order("message_id").Pluck("message_id", &remaining).Error; err != nil {
		t.Fatalf("read remaining rows: %v", err)
	}
	want := []string{"m-foreign-done", "m-old-failed", "m-recent-done"}
	if strings.Join(remaining, ",") != strings.Join(want, ",") {
		t.Fatalf("remaining rows = %v, want %v", remaining, want)
	}

	if _, err := proc.Prune(context.Background(), 0); !errors.Is(err, ErrInvalidPruneWindow) {
		t.Fatalf("Prune(0) = %v, want ErrInvalidPruneWindow", err)
	}
}

// TestPruneWithoutIdempotencyIsANoop pins the maintenance contract for a
// consumer that never built a ledger: asking it to prune removes nothing and
// reports no error, so a scheduled job can call it unconditionally.
// TestPruneWithoutIdempotencyIsANoop 固定"从未建台账"消费者的维护契约：调用清理什么都不删、
// 也不报错，定时任务因此可以无条件下调。
func TestPruneWithoutIdempotencyIsANoop(t *testing.T) {
	rt := core.NewRuntime(core.RuntimeConfig{Logger: zap.NewNop()})
	proc, err := NewProcessor(rt, ProcessorConfig{
		Consumer: "no-ledger",
		Handler:  func(context.Context, Message, Envelope) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	n, err := proc.Prune(context.Background(), time.Hour)
	if err != nil || n != 0 {
		t.Fatalf("Prune without idempotency = (%d, %v), want (0, nil)", n, err)
	}
}
