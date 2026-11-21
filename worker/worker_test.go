package worker_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bingcool/gofy/worker"
)

func TestNewGroupValidates(t *testing.T) {
	ok := worker.Func("order-scan", 1, func(context.Context) error { return nil })

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
		{"blank worker name", "order", []worker.Worker{worker.Func("", 1, func(context.Context) error { return nil })}, worker.ErrNameRequired},
		{"bad worker name", "order", []worker.Worker{worker.Func("Order_Scan", 1, func(context.Context) error { return nil })}, worker.ErrInvalidName},
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
	scan := worker.Func("order-scan", 1, func(context.Context) error { return nil })
	notify := worker.Func("order-notify", 1, func(context.Context) error { return nil })
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
	w := worker.Func("order-scan", 1, func(ctx context.Context) error {
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
	w := worker.Func("flaky", 1, func(ctx context.Context) error {
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
	w := worker.Func("broken", 1, func(context.Context) error {
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
	w := worker.Func("broken", 1, func(context.Context) error {
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

// The fatal error used to live in a plain variable written from once.Do and
// read after select picked ctx.Done(). Those two had no happens-before edge
// when the drain also timed out. Drive that exact interleaving repeatedly so
// -race exercises the handoff: one worker fails at once, another ignores the
// shutdown context and keeps the drain from completing.
// fatal 过去存在普通变量里，由 once.Do 写入、select 选中 ctx.Done() 后读取；当 drain 也超时时，
// 两者之间没有 happens-before。这里反复驱动这个交错：一个 worker 立刻失败，另一个忽略停机 ctx
// 让 drain 无法完成，从而让 -race 覆盖这次交接。
func TestRunnerFatalRacesWithCancel(t *testing.T) {
	// Each iteration waits for the runner's fixed one-second drain budget, so a
	// handful of rounds is enough to exercise the interleaving without making
	// the suite slow.
	// 每轮都要等 runner 固定的一秒 drain 预算，几轮就足以覆盖这个交错，不让测试变慢。
	for i := 0; i < 5; i++ {
		release := make(chan struct{})
		failer := worker.Func("failer", 1, func(context.Context) error {
			return errors.New("boom")
		})
		blocker := worker.Func("blocker", 1, func(ctx context.Context) error {
			<-ctx.Done()
			<-release
			return nil
		})
		runner, err := worker.NewRunner(mustGroup(t, "g", failer, blocker), worker.RunConfig{
			Policy:       worker.Policy{Mode: worker.ExitOnError},
			DrainTimeout: 10 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- runner.Run(ctx) }()
		cancel()

		select {
		case err := <-done:
			// Either result is correct: if select took ctx.Done() before the
			// worker reported, cancellation wins by design. The value of this
			// test is that -race sees the interleaving without a warning.
			// 两种返回值都正确：select 若先选中 ctx.Done()，按设计取消优先。这个测试的价值
			// 在于让 -race 看到这个交错而不报竞态，而不是规定返回值。
			if err != nil && !strings.Contains(err.Error(), "boom") {
				t.Fatalf("iteration %d: unexpected Run error = %v", i, err)
			}
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatalf("iteration %d: Run did not return", i)
		}
		close(release)
	}
}

func TestRunnerRejectsEarlyReturn(t *testing.T) {
	w := worker.Func("quick", 1, func(context.Context) error { return nil })
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
	w := worker.Func("drainy", 1, func(ctx context.Context) error {
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
	err := worker.Func("noop", 1, nil).Run(context.Background())
	if !errors.Is(err, worker.ErrNilRun) {
		t.Fatalf("Run error = %v, want ErrNilRun", err)
	}
}

// TestDrainContextDoesNotExpireBeforeShutdown pins a regression: the drain
// budget used to start when the runner started, so any worker running longer
// than DrainTimeout found its drain context already expired and failed every
// in-flight handler with context.DeadlineExceeded.
// TestDrainContextDoesNotExpireBeforeShutdown 固定一个回归：drain 预算过去从
// runner 启动时起算，导致运行时间超过 DrainTimeout 的 worker 发现 drain context
// 早已过期，每个在途 handler 都返回 context.DeadlineExceeded。
func TestDrainContextDoesNotExpireBeforeShutdown(t *testing.T) {
	checked := make(chan error, 1)
	w := worker.Func("long-lived", 1, func(ctx context.Context) error {
		// Outlive the drain budget while the process is perfectly healthy.
		// 在进程完全健康的情况下活得比 drain 预算更久。
		select {
		case <-time.After(150 * time.Millisecond):
		case <-ctx.Done():
			return nil
		}
		drain := worker.DrainContext(ctx)
		select {
		case <-drain.Done():
			checked <- fmt.Errorf("drain context expired before shutdown: %w", drain.Err())
		default:
			checked <- nil
		}
		<-ctx.Done()
		return nil
	})
	runner, err := worker.NewRunner(mustGroup(t, "g", w), worker.RunConfig{
		DrainTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("Run did not return after cancellation")
		}
	}()

	select {
	case err := <-checked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker never reported the drain state")
	}
}

func TestNewRunnerRejectsInvalidInput(t *testing.T) {
	if _, err := worker.NewRunner(nil, worker.RunConfig{}); !errors.Is(err, worker.ErrNilGroup) {
		t.Fatalf("NewRunner(nil) error = %v, want ErrNilGroup", err)
	}
	group := mustGroup(t, "g", worker.Func("w", 1, func(context.Context) error { return nil }))
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

// TestFuncCarriesConcurrency pins the reason Func takes a count: the worker
// knows how many instances it needs, so the host does not have to keep a
// "group/worker" table in sync with it.
// TestFuncCarriesConcurrency 固定 Func 接收计数的理由：worker 自己知道要跑几个实例，
// 宿主不必再维护一张与它同步的 "group/worker" 表。
func TestFuncCarriesConcurrency(t *testing.T) {
	w := worker.Func("order-scan", 3, func(context.Context) error { return nil })
	c, ok := w.(worker.Concurrent)
	if !ok {
		t.Fatal("Func worker does not implement worker.Concurrent")
	}
	if c.Concurrency() != 3 {
		t.Fatalf("Concurrency() = %d, want 3", c.Concurrency())
	}
}

// TestNewGroupRejectsZeroConcurrency keeps the uint8 honest: Func has no error
// return, so the group is the first place a zero count can be reported.
// TestNewGroupRejectsZeroConcurrency 守住 uint8 的底线：Func 没有错误返回，分组是第一个
// 能报出 0 计数的地方。
func TestNewGroupRejectsZeroConcurrency(t *testing.T) {
	_, err := worker.NewGroup("order",
		worker.Func("scan", 0, func(context.Context) error { return nil }),
	)
	if !errors.Is(err, worker.ErrInvalidConcurrency) {
		t.Fatalf("NewGroup error = %v, want ErrInvalidConcurrency", err)
	}
}

// TestRunnerUsesWorkerConcurrency is the behaviour the option map used to
// provide: a worker that declares two instances starts twice with no host
// configuration at all.
// TestRunnerUsesWorkerConcurrency 是过去靠选项映射提供的行为：worker 自己声明两个
// 实例时，完全不需要宿主配置就会启动两次。
func TestRunnerUsesWorkerConcurrency(t *testing.T) {
	ready := make(chan struct{}, 3)
	w := worker.Func("scan", 3, func(ctx context.Context) error {
		ready <- struct{}{}
		<-ctx.Done()
		return nil
	})
	runner, err := worker.NewRunner(mustGroup(t, "g", w), worker.RunConfig{
		DrainTimeout: 100 * time.Millisecond,
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
			t.Fatalf("only %d instances started, want 3", i)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}
