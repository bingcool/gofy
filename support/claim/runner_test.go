package claim

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestNewRunnerRequiresHandler(t *testing.T) {
	_, err := NewRunner(new(gorm.DB), RunnerConfig{Config: Config{Table: "job_task", Job: "order"}})
	if !errors.Is(err, ErrHandlerRequired) {
		t.Fatalf("NewRunner without handler error = %v, want ErrHandlerRequired", err)
	}
}

func TestNewRunnerAppliesDefaultsAndKeepsConfig(t *testing.T) {
	called := false
	r, err := NewRunner(new(gorm.DB), RunnerConfig{
		Config:  Config{Table: "job_task", Job: "order"},
		Handler: func(context.Context, []byte) (uint64, error) { called = true; return 0, nil },
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if r.handlerTimeout != DefaultHandlerTimeout {
		t.Fatalf("handlerTimeout = %v, want %v", r.handlerTimeout, DefaultHandlerTimeout)
	}
	// Attempts live on the queue now: Fail reports whether it parked the row, so
	// Runner no longer keeps its own copy of the cap.
	// 尝试次数只留在队列上：Fail 会告诉调用方是否置死，Runner 不再自己存一份上限。
	if got := r.queue.MaxAttempts(); got != DefaultMaxAttempts {
		t.Fatalf("queue maxAttempts = %d, want %d", got, DefaultMaxAttempts)
	}
	if r.queue.Job() != "order" {
		t.Fatalf("queue job = %q, want order", r.queue.Job())
	}
	// The handler is stored, not called, at construction time.
	if called {
		t.Fatal("Handler was called during NewRunner")
	}
}

func TestNewRunnerRejectsInvalidQueueConfig(t *testing.T) {
	// NewRunner must pass Config validation through, not just check the handler.
	_, err := NewRunner(new(gorm.DB), RunnerConfig{
		Config:  Config{Table: "bad table name", Job: "order"},
		Handler: func(context.Context, []byte) (uint64, error) { return 0, nil },
	})
	if !errors.Is(err, ErrInvalidTable) {
		t.Fatalf("NewRunner with bad table error = %v, want ErrInvalidTable", err)
	}
}

func TestNewRunnerKeepsExplicitTimeouts(t *testing.T) {
	r, err := NewRunner(new(gorm.DB), RunnerConfig{
		Config:         Config{Table: "job_task", Job: "order"},
		HandlerTimeout: 50 * time.Millisecond,
		Handler:        func(context.Context, []byte) (uint64, error) { return 0, nil },
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if r.handlerTimeout != 50*time.Millisecond {
		t.Fatalf("handlerTimeout = %v, want 50ms", r.handlerTimeout)
	}
}

// TestNewRunnerRejectsHandlerTimeoutNotBelowLease locks in the construction-time
// guard: Extend only protects the queueing window, so a handler allowed to run
// past its lease can be executed twice. The pair must be rejected up front.
// TestNewRunnerRejectsHandlerTimeoutNotBelowLease 锁定构造期校验：Extend 只保护排队窗口，
// 允许跑过租约的 handler 可能被执行两遍，这种组合必须当场拒绝。
func TestNewRunnerRejectsHandlerTimeoutNotBelowLease(t *testing.T) {
	for name, cfg := range map[string]Config{
		"equal": {Table: "job_task", Job: "order", Lease: 5 * time.Second},
		"over":  {Table: "job_task", Job: "order", Lease: 4 * time.Second},
	} {
		_, err := NewRunner(new(gorm.DB), RunnerConfig{
			Config:         cfg,
			HandlerTimeout: 5 * time.Second,
			Handler:        func(context.Context, []byte) (uint64, error) { return 0, nil },
		})
		if !errors.Is(err, ErrHandlerTimeoutExceedsLease) {
			t.Fatalf("%s: NewRunner error = %v, want ErrHandlerTimeoutExceedsLease", name, err)
		}
	}

	// Just below the lease is accepted.
	if _, err := NewRunner(new(gorm.DB), RunnerConfig{
		Config:         Config{Table: "job_task", Job: "order", Lease: 5 * time.Second},
		HandlerTimeout: 4 * time.Second,
		Handler:        func(context.Context, []byte) (uint64, error) { return 0, nil },
	}); err != nil {
		t.Fatalf("NewRunner with timeout below lease: %v", err)
	}
}

// TestRunnerSQLWiresSucceedAndFail guards the core promise of this layer: a
// successful handler closes the row (which stamps finish_handle_time) and a
// failing one records reason + time (which stamps fail_reason and
// fail_handle_time). It checks the statements the Runner will execute without
// touching a database.
func TestRunnerSQLWiresSucceedAndFail(t *testing.T) {
	r, err := NewRunner(new(gorm.DB), RunnerConfig{
		Config:  Config{Table: "job_task", Job: "order"},
		Handler: func(context.Context, []byte) (uint64, error) { return 0, nil },
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	checks := map[string]string{
		"succeed": r.queue.succeedSQL,
		"dead":    r.queue.deadSQL,
		"fail":    r.queue.failSQL,
	}
	for name, sql := range checks {
		if !strings.Contains(sql, "status =") || !strings.Contains(sql, "attempts = ?") {
			t.Fatalf("%sSQL is not a fenced state write: %s", name, sql)
		}
	}
	if !strings.Contains(r.queue.succeedSQL, "finish_handle_time = CURRENT_TIMESTAMP") {
		t.Fatalf("succeedSQL does not stamp finish_handle_time: %s", r.queue.succeedSQL)
	}
	if !strings.Contains(r.queue.failSQL, "fail_reason = ?") ||
		!strings.Contains(r.queue.deadSQL, "fail_handle_time = CURRENT_TIMESTAMP") {
		t.Fatalf("fail statements do not record reason and time:\nfail: %s\ndead: %s",
			r.queue.failSQL, r.queue.deadSQL)
	}
}

// The idle round is user code too: it must run under the same HandlerTimeout as
// a real row. Without the deadline a heartbeat that blocks on a slow call would
// hold the polling goroutine until it returned, no matter how long that took.
// 空闲轮同样是用户代码：必须和真实任务一样受 HandlerTimeout 约束。没有这个 deadline
// 时，阻塞在慢调用上的心跳会一直占住轮询 goroutine。
func TestIdleCallbackHonoursHandlerTimeout(t *testing.T) {
	var gotNil bool
	var hadDeadline bool
	released := make(chan struct{})
	r, err := NewRunner(new(gorm.DB), RunnerConfig{
		Config:         Config{Table: "job_task", Job: "order"},
		HandlerTimeout: 20 * time.Millisecond,
		Handler: func(ctx context.Context, data []byte) (uint64, error) {
			gotNil = data == nil
			_, hadDeadline = ctx.Deadline()
			<-ctx.Done()
			close(released)
			return 0, errors.New("ignored: there is no row to settle")
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.idle(context.Background())
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("idle did not return: HandlerTimeout was not applied to the nil-data call")
	}
	select {
	case <-released:
	default:
		t.Fatal("idle returned before the handler finished")
	}
	if !gotNil {
		t.Fatal("idle call did not pass nil data")
	}
	if !hadDeadline {
		t.Fatal("idle ctx carried no deadline")
	}
}
