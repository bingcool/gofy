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

// TestReadyGateIgnoresASecondSignalFromTheSameInstance pins the accounting: a
// gate of two instances closes only after both reported, even when the first
// one reports twice.
// TestReadyGateIgnoresASecondSignalFromTheSameInstance 固定记账规则：两个实例的闸门
// 只有在两者都上报后才关闭，即使第一个上报了两次。
func TestReadyGateIgnoresASecondSignalFromTheSameInstance(t *testing.T) {
	a := Identity{Group: "orders", Name: "consumer", Index: 0, Total: 2}
	b := Identity{Group: "orders", Name: "consumer", Index: 1, Total: 2}

	gate := newReadyGate()
	gate.add([]Identity{a, b})

	gate.signal(a)
	gate.signal(a)
	select {
	case <-gate.wait():
		t.Fatal("a duplicate signal from instance 0 closed the gate for instance 1")
	default:
	}

	gate.signal(b)
	select {
	case <-gate.wait():
	default:
		t.Fatal("gate stayed open after both instances reported")
	}
}

// doubleReportingWorker reports twice from instance 0 — what an adapter
// reconnecting inside one Run, or the runner restarting the same instance,
// produces on the wire.
// doubleReportingWorker 让实例 0 上报两次——适配器在一次 Run 内重连，或 runner 重启
// 同一实例时，线上看到的就是这种信号。
type doubleReportingWorker struct {
	first  chan struct{}
	second chan struct{}
}

func (w *doubleReportingWorker) Name() string  { return "consumer" }
func (w *doubleReportingWorker) ReportsReady() {}

func (w *doubleReportingWorker) Run(ctx context.Context) error {
	id, _ := IdentityFrom(ctx)
	wait := w.first
	if id.Index > 0 {
		wait = w.second
	}
	select {
	case <-wait:
	case <-ctx.Done():
		return nil
	}
	Ready(ctx)
	if id.Index == 0 {
		Ready(ctx)
	}
	<-ctx.Done()
	return nil
}

// TestReadyIsNotClosedByARepeatedSignal is the runner-level version of the
// property P1-05 asked for: a worker that reports twice must not complete the
// group's readiness on behalf of an instance that never connected.
// TestReadyIsNotClosedByARepeatedSignal 是 P1-05 在 runner 层的体现：重复上报的
// worker 不能替一个从未连上的实例把分组的就绪状态完成掉。
func TestReadyIsNotClosedByARepeatedSignal(t *testing.T) {
	w := &doubleReportingWorker{first: make(chan struct{}), second: make(chan struct{})}
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

	close(w.first)
	select {
	case <-runner.Ready():
		t.Fatal("a repeated signal from instance 0 made the group ready before instance 1 connected")
	case <-time.After(100 * time.Millisecond):
	}

	close(w.second)
	select {
	case <-runner.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("group did not report ready after both instances connected")
	}
}
