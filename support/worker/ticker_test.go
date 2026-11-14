package worker_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/worker"
)

// TestTickerRunsUntilCancelled covers the contract a resident loop needs: it
// keeps calling the callback, and cancellation is a clean return rather than an
// error the host would treat as a crash.
// TestTickerRunsUntilCancelled 覆盖常驻循环需要的契约：回调会被反复调用，取消时是干净的
// 返回，而不是宿主会当成崩溃的错误。
func TestTickerRunsUntilCancelled(t *testing.T) {
	var calls int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- worker.Ticker(ctx, 5, func(context.Context) error {
			atomic.AddInt32(&calls, 1)
			return nil
		})
	}()

	waitCalls(t, &calls, 3)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Ticker returned %v, want nil after cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ticker did not return after cancellation")
	}
}

// TestTickerKeepsGoingAfterFailures is the property that makes it safe to use in
// a long-running worker: one failed aggregation must not end the loop, otherwise
// the process stays up while the work has quietly stopped.
// TestTickerKeepsGoingAfterFailures 是它能在常驻 worker 里使用的前提：一次失败的聚合
// 不能结束循环，否则进程还在、活已经悄悄停了。
func TestTickerKeepsGoingAfterFailures(t *testing.T) {
	var calls int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- worker.Ticker(ctx, 5, func(context.Context) error {
			atomic.AddInt32(&calls, 1)
			return errors.New("boom")
		})
	}()

	waitCalls(t, &calls, 3)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Ticker returned %v, want nil: a failing callback is logged, not fatal", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ticker stopped after a callback error")
	}
}

// TestTickerRecoversPanics keeps a bad tick from taking the whole worker down:
// the panic is contained to that call and the next tick still runs.
// TestTickerRecoversPanics 让坏 tick 带不走整个 worker：panic 被限制在那一次调用里，
// 下一个 tick 照常执行。
func TestTickerRecoversPanics(t *testing.T) {
	var calls int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- worker.Ticker(ctx, 5, func(context.Context) error {
			if atomic.AddInt32(&calls, 1) <= 2 {
				panic("boom")
			}
			return nil
		})
	}()

	waitCalls(t, &calls, 3)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Ticker returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ticker stopped after a panic")
	}
}

func TestTickerRejectsBadInput(t *testing.T) {
	fn := func(context.Context) error { return nil }
	if err := worker.Ticker(context.Background(), 0, fn); err == nil {
		t.Fatal("Ticker accepted a zero interval")
	}
	if err := worker.Ticker(context.Background(), 5000, nil); err == nil {
		t.Fatal("Ticker accepted a nil callback")
	}
}

func waitCalls(t *testing.T, calls *int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(calls) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("callback ran %d times, want at least %d", atomic.LoadInt32(calls), want)
}
