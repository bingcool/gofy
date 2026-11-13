package worker_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/worker"
)

func TestNewGroupValidates(t *testing.T) {
	ok := worker.Func("order-scan", func(context.Context) error { return nil })

	tests := []struct {
		name    string
		group   string
		workers []worker.Worker
		want    error
	}{
		{"empty group name", "", []worker.Worker{ok}, worker.ErrNameRequired},
		{"bad group name", "Order", []worker.Worker{ok}, worker.ErrInvalidName},
		{"no workers", "order", nil, worker.ErrNoWorkers},
		{"nil worker", "order", []worker.Worker{nil}, worker.ErrNilWorker},
		{"blank worker name", "order", []worker.Worker{worker.Func("", func(context.Context) error { return nil })}, worker.ErrNameRequired},
		{"bad worker name", "order", []worker.Worker{worker.Func("Order_Scan", func(context.Context) error { return nil })}, worker.ErrInvalidName},
		{"duplicate worker", "order", []worker.Worker{ok, ok}, worker.ErrDuplicateWorker},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := worker.NewGroup(tc.group, tc.workers...)
			if !errors.Is(err, tc.want) {
				t.Fatalf("NewGroup error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestGroupSubset(t *testing.T) {
	scan := worker.Func("order-scan", func(context.Context) error { return nil })
	notify := worker.Func("order-notify", func(context.Context) error { return nil })
	group, err := worker.NewGroup("order", scan, notify)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}

	subset, err := group.Subset([]string{"order-notify"})
	if err != nil {
		t.Fatalf("Subset: %v", err)
	}
	if names := namesOf(subset); len(names) != 1 || names[0] != "order-notify" {
		t.Fatalf("subset = %v, want [order-notify]", names)
	}

	if _, err := group.Subset([]string{"missing"}); !errors.Is(err, worker.ErrUnknownWorker) {
		t.Fatalf("Subset error = %v, want ErrUnknownWorker", err)
	}

	all, err := group.Subset(nil)
	if err != nil {
		t.Fatalf("Subset(nil): %v", err)
	}
	if names := namesOf(all); len(names) != 2 {
		t.Fatalf("Subset(nil) = %v, want both workers", names)
	}
}

func TestRunnerRunsInstancesWithIdentity(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []worker.Identity
	)
	ready := make(chan struct{}, 4)
	w := worker.Func("order-scan", func(ctx context.Context) error {
		if id, ok := worker.IdentityFrom(ctx); ok {
			mu.Lock()
			seen = append(seen, id)
			mu.Unlock()
		}
		ready <- struct{}{}
		<-ctx.Done()
		return nil
	})
	group, err := worker.NewGroup("order", w)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	runner, err := worker.NewRunner(group, worker.RunConfig{
		Concurrency:  map[string]int{"order-scan": 3},
		DrainTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	for i := 0; i < 3; i++ {
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d instances started", i)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after cancellation")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("instances = %d, want 3", len(seen))
	}
	indexes := map[int]bool{}
	for _, id := range seen {
		if id.Group != "order" || id.Name != "order-scan" || id.Total != 3 {
			t.Fatalf("identity = %+v", id)
		}
		indexes[id.Index] = true
	}
	if len(indexes) != 3 {
		t.Fatalf("indexes = %v, want 0,1,2", indexes)
	}
}

func TestRunnerRestartsUntilSuccess(t *testing.T) {
	var calls int32
	w := worker.Func("flaky", func(ctx context.Context) error {
		if atomic.AddInt32(&calls, 1) < 3 {
			return errors.New("transient")
		}
		<-ctx.Done()
		return nil
	})
	runner, err := worker.NewRunner(mustGroup(t, "g", w), worker.RunConfig{
		Policy:       worker.Policy{Mode: worker.RestartOnError, Backoff: time.Millisecond},
		DrainTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&calls) < 3 {
		select {
		case <-deadline:
			t.Fatalf("worker restarted %d times, want 3 calls", atomic.LoadInt32(&calls))
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunnerStopsAfterRestartLimit(t *testing.T) {
	var calls int32
	w := worker.Func("broken", func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("always")
	})
	runner, err := worker.NewRunner(mustGroup(t, "g", w), worker.RunConfig{
		Policy: worker.Policy{
			Mode:        worker.RestartOnError,
			Backoff:     time.Millisecond,
			MaxRestarts: 1,
		},
		DrainTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	err = runner.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "restart limit") {
		t.Fatalf("Run error = %v, want restart limit", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2 (initial + one restart)", got)
	}
}

func TestRunnerExitsOnErrorInFatalMode(t *testing.T) {
	var calls int32
	w := worker.Func("broken", func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("fatal")
	})
	runner, err := worker.NewRunner(mustGroup(t, "g", w), worker.RunConfig{
		Policy:       worker.Policy{Mode: worker.ExitOnError},
		DrainTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	if err := runner.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "fatal") {
		t.Fatalf("Run error = %v, want fatal", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}

func TestRunnerRejectsEarlyReturn(t *testing.T) {
	w := worker.Func("quick", func(context.Context) error { return nil })
	runner, err := worker.NewRunner(mustGroup(t, "g", w), worker.RunConfig{
		DrainTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	err = runner.Run(context.Background())
	if !errors.Is(err, worker.ErrWorkerExited) {
		t.Fatalf("Run error = %v, want ErrWorkerExited", err)
	}
}

func TestRunnerDrainsBeforeReturning(t *testing.T) {
	drainAlive := make(chan bool, 1)
	finished := make(chan time.Time, 1)
	w := worker.Func("drainy", func(ctx context.Context) error {
		<-ctx.Done()
		drain := worker.DrainContext(ctx)
		select {
		case <-drain.Done():
			drainAlive <- false
		default:
			drainAlive <- true
		}
		time.Sleep(20 * time.Millisecond)
		finished <- time.Now()
		return nil
	})
	runner, err := worker.NewRunner(mustGroup(t, "g", w), worker.RunConfig{
		DrainTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return")
	}
	if !<-drainAlive {
		t.Fatalf("drain context was already cancelled when the worker observed shutdown")
	}
	select {
	case <-finished:
	default:
		t.Fatalf("runner returned before the in-flight work finished")
	}
}

func TestFuncWithoutRun(t *testing.T) {
	err := worker.Func("noop", nil).Run(context.Background())
	if !errors.Is(err, worker.ErrNilRun) {
		t.Fatalf("Run error = %v, want ErrNilRun", err)
	}
}

func TestNewRunnerRejectsInvalidInput(t *testing.T) {
	if _, err := worker.NewRunner(nil, worker.RunConfig{}); !errors.Is(err, worker.ErrNilGroup) {
		t.Fatalf("NewRunner(nil) error = %v, want ErrNilGroup", err)
	}
	group := mustGroup(t, "g", worker.Func("w", func(context.Context) error { return nil }))
	if _, err := worker.NewRunner(group, worker.RunConfig{Concurrency: map[string]int{"w": 0}}); !errors.Is(err, worker.ErrInvalidConcurrency) {
		t.Fatalf("NewRunner error = %v, want ErrInvalidConcurrency", err)
	}
}

func mustGroup(t *testing.T, name string, workers ...worker.Worker) *worker.Group {
	t.Helper()
	group, err := worker.NewGroup(name, workers...)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	return group
}

func namesOf(group *worker.Group) []string {
	workers := group.Workers()
	names := make([]string, len(workers))
	for i, w := range workers {
		names[i] = w.Name()
	}
	return names
}
