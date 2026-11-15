package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

// reportingWorker connects before it accepts work and reports through Ready.
type reportingWorker struct {
	name    string
	connect chan struct{}
	fail    error
}

func (w *reportingWorker) Name() string  { return w.name }
func (w *reportingWorker) ReportsReady() {}

func (w *reportingWorker) Run(ctx context.Context) error {
	if w.fail != nil {
		return w.fail
	}
	select {
	case <-w.connect:
		Ready(ctx)
	case <-ctx.Done():
		return nil
	}
	<-ctx.Done()
	return nil
}

func TestReadyWaitsForEveryReportingInstance(t *testing.T) {
	w := &reportingWorker{name: "consumer", connect: make(chan struct{})}
	group, err := NewGroup("orders", w)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	runner, err := NewRunner(group, RunConfig{Concurrency: map[string]int{"consumer": 2}})
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
		case <-time.After(5 * time.Second):
			t.Fatal("runner did not stop")
		}
	}()

	// Both instances are blocked in connect, so nothing may report ready.
	// 两个实例都卡在 connect，因此还不允许报告就绪。
	select {
	case <-runner.Ready():
		t.Fatal("group reported ready before its instances connected")
	case <-time.After(50 * time.Millisecond):
	}

	close(w.connect)
	select {
	case <-runner.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("group did not report ready after every instance connected")
	}
}

func TestReadyIsImmediateWithoutReportingWorkers(t *testing.T) {
	// A polling loop has nothing to connect, so it must not delay readiness:
	// that would make every existing Func group wait for nothing.
	// 轮询任务没有要连的东西，因此不能拖慢就绪：否则所有现有的 Func 分组都会白等。
	group, err := NewGroup("orders", Func("scan", 1, func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}))
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	runner, err := NewRunner(group, RunConfig{})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	select {
	case <-runner.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("group without reporting workers did not report ready")
	}
	cancel()
	<-done
}

func TestReadyStaysClosedWhenStartupFails(t *testing.T) {
	// A failed start must never look like readiness: the caller waits on Ready
	// together with the error channel, and a closed channel here would make the
	// probe green for a process that consumes nothing.
	// 启动失败绝不能看起来像"已就绪"：调用方会同时等 Ready 与错误通道，这里若关闭
	// 通道，就会让一个什么都没消费的进程探针变绿。
	w := &reportingWorker{name: "consumer", fail: errors.New("broker unreachable")}
	group, err := NewGroup("orders", w)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	runner, err := NewRunner(group, RunConfig{Policy: Policy{Mode: ExitOnError}})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	if err := runner.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded although the worker failed to start")
	}
	select {
	case <-runner.Ready():
		t.Fatal("Ready was closed for a group whose startup failed")
	default:
	}
}
