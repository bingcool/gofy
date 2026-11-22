package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// restartingReporter reports ready, fails once to force a restart, then blocks
// before reporting again. The two gates make both readiness edges observable:
// the first Run blocks after reporting, the second blocks before.
// restartingReporter 上报就绪，然后失败一次以触发重启，第二次上报前先阻塞。两道闸门
// 让两次就绪边沿都可观察：第一次 Run 在上报后阻塞，第二次在上报前阻塞。
type restartingReporter struct {
	attempt     atomic.Int32
	firstReady  chan struct{}
	proceed     chan struct{}
	secondAllow chan struct{}
	secondReady chan struct{}
}

func (w *restartingReporter) Name() string  { return "consumer" }
func (w *restartingReporter) ReportsReady() {}

func (w *restartingReporter) Run(ctx context.Context) error {
	if w.attempt.Add(1) == 1 {
		Ready(ctx)
		close(w.firstReady)
		select {
		case <-w.proceed:
			return errors.New("subscription lost")
		case <-ctx.Done():
			return nil
		}
	}
	select {
	case <-w.secondAllow:
	case <-ctx.Done():
		return nil
	}
	Ready(ctx)
	close(w.secondReady)
	<-ctx.Done()
	return nil
}

func waitForReady(t *testing.T, runner *Runner, want bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runner.IsReady() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("IsReady() never became %v", want)
}

// TestRunnerTurnsNotReadyWhileAnInstanceRestarts pins the restart edge: after an
// instance reported ready and then failed, the group must answer not-ready
// until the new subscription reports again. Before this was fixed the first
// readiness was final, so a crash-looping consumer kept the probe green.
// TestRunnerTurnsNotReadyWhileAnInstanceRestarts 钉住重启边沿：实例上报就绪后失败，
// 分组必须在新订阅重新上报之前回答未就绪。修复之前首次就绪是终态，崩溃重启中的消费者
// 会让探针一直保持绿色。
func TestRunnerTurnsNotReadyWhileAnInstanceRestarts(t *testing.T) {
	w := &restartingReporter{
		firstReady:  make(chan struct{}),
		proceed:     make(chan struct{}),
		secondAllow: make(chan struct{}),
		secondReady: make(chan struct{}),
	}
	group, err := NewGroup("orders", w)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	runner, err := NewRunner(group, RunConfig{
		Policy:       Policy{Backoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond},
		DrainTimeout: time.Second,
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
		case <-time.After(5 * time.Second):
			t.Fatal("runner did not stop")
		}
	}()

	<-w.firstReady
	waitForReady(t, runner, true)

	// The first Run fails; the runner retracts the instance before backing off.
	// 第一次 Run 失败；runner 在退避之前把该实例移出就绪集合。
	close(w.proceed)
	waitForReady(t, runner, false)

	// The second Run is held before Ready, so the not-ready window is stable.
	// 第二次 Run 在上报之前被挡住，未就绪窗口因此是稳定的。
	close(w.secondAllow)
	<-w.secondReady
	waitForReady(t, runner, true)
}

// TestReadyGateRearmsAfterRetract covers the gate itself: retract moves an
// instance back into pending, the first-readiness channel stays closed, and the
// next signal settles again so the live flag can recover.
// TestReadyGateRearmsAfterRetract 覆盖闸门本身：retract 把实例放回待上报集合，首次
// 就绪通道保持关闭，下一次上报会再次排空，实时标志因此可以恢复。
func TestReadyGateRearmsAfterRetract(t *testing.T) {
	gate := newReadyGate()
	settled := 0
	gate.onSettled = func() { settled++ }
	id := Identity{Group: "orders", Name: "consumer", Index: 0}

	gate.add([]Identity{id})
	if settled != 0 {
		t.Fatalf("gate settled before the instance reported (settled=%d)", settled)
	}
	gate.signal(id)
	if settled != 1 {
		t.Fatalf("settled after first signal = %d, want 1", settled)
	}
	select {
	case <-gate.wait():
	default:
		t.Fatal("first-readiness channel is not closed")
	}

	gate.retract(id)
	select {
	case <-gate.wait():
	default:
		t.Fatal("retract must not reopen the first-readiness channel")
	}
	gate.signal(id)
	if settled != 2 {
		t.Fatalf("settled after re-signal = %d, want 2", settled)
	}
}
