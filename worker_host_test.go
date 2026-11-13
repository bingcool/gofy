package gofy_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support/worker"
)

func TestExecuteWorkerRequiresGroup(t *testing.T) {
	app := newWorkerApp(t, "order", new(int32))
	err := app.ExecuteArgs(context.Background(), []string{"worker"})
	if !errors.Is(err, gofy.ErrUsage) {
		t.Fatalf("ExecuteArgs error = %v, want ErrUsage", err)
	}
}

func TestExecuteWorkerUnknownGroup(t *testing.T) {
	app := newWorkerApp(t, "order", new(int32))
	err := app.ExecuteArgs(context.Background(), []string{"worker", "--group=missing"})
	if !errors.Is(err, gofy.ErrWorkerGroupNotFound) {
		t.Fatalf("ExecuteArgs error = %v, want ErrWorkerGroupNotFound", err)
	}
}

func TestExecuteWorkerListsGroups(t *testing.T) {
	app := newWorkerApp(t, "order", new(int32))
	if err := app.ExecuteArgs(context.Background(), []string{"worker", "--list"}); err != nil {
		t.Fatalf("ExecuteArgs error = %v", err)
	}
}

func TestExecuteWorkerRunsConcurrentInstancesUntilCanceled(t *testing.T) {
	var started int32
	app := newWorkerApp(t, "order", &started)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- app.ExecuteArgs(ctx, []string{"worker", "--group=order", "--concurrency=2"})
	}()

	waitFor(t, func() bool { return atomic.LoadInt32(&started) == 2 }, "two instances to start")
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ExecuteArgs error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("worker command did not stop after cancellation")
	}
}

func TestExecuteWorkerNameFilter(t *testing.T) {
	var primary, secondary int32
	primaryWorker := worker.Func("primary", func(ctx context.Context) error {
		atomic.AddInt32(&primary, 1)
		<-ctx.Done()
		return nil
	})
	secondaryWorker := worker.Func("secondary", func(ctx context.Context) error {
		atomic.AddInt32(&secondary, 1)
		<-ctx.Done()
		return nil
	})
	group, err := worker.NewGroup("order", primaryWorker, secondaryWorker)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	app, err := gofy.New(
		gofy.WithName("worker-test"),
		gofy.WithWorkerGroups(group),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- app.ExecuteArgs(ctx, []string{"worker", "--group=order", "--name=primary"})
	}()

	waitFor(t, func() bool { return atomic.LoadInt32(&primary) == 1 }, "primary to start")
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&secondary); got != 0 {
		t.Fatalf("secondary started %d times, want 0", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("ExecuteArgs error = %v", err)
	}
}

// newWorkerApp builds an app with one blocking worker that increments started on
// each launch, so a test can observe the resolved concurrency.
// newWorkerApp 构造一个应用：分组里有一个阻塞型 worker，每次启动自增 started，
// 测试因此能观察到解析出来的并发数。
func newWorkerApp(t *testing.T, groupName string, started *int32) *gofy.App {
	t.Helper()
	group, err := worker.NewGroup(groupName, worker.Func("tick", func(ctx context.Context) error {
		atomic.AddInt32(started, 1)
		<-ctx.Done()
		return nil
	}))
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	app, err := gofy.New(
		gofy.WithName("worker-test"),
		gofy.WithWorkerGroups(group),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
