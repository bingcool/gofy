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
	if r.maxAttempts != DefaultMaxAttempts {
		t.Fatalf("maxAttempts = %d, want %d", r.maxAttempts, DefaultMaxAttempts)
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
