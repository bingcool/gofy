package worker

import (
	"context"
	"testing"
	"time"
)

// stubbornWorker ignores its context, which is the handler violation the drain
// budget exists to survive: its instance goroutine outlives Run.
// stubbornWorker 忽略 context，正是 drain 预算要兜住的违约 handler：它的实例
// goroutine 会比 Run 活得久。
type stubbornWorker struct {
	name    string
	release chan struct{}
}

func (w *stubbornWorker) Name() string { return w.name }

func (w *stubbornWorker) Run(context.Context) error {
	<-w.release
	return nil
}

// politeWorker stops as soon as the context is cancelled.
// politeWorker 在 context 取消后立刻停止。
type politeWorker struct{ name string }

func (w *politeWorker) Name() string { return w.name }

func (w *politeWorker) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// TestRunnerReportsAbandonedInstancesWhenTheDrainBudgetRunsOut: the host has to
// know that instances are still running before it closes the database under
// them.
// TestRunnerReportsAbandonedInstancesWhenTheDrainBudgetRunsOut：宿主必须在关闭
// 数据库之前知道仍有实例在跑。
func TestRunnerReportsAbandonedInstancesWhenTheDrainBudgetRunsOut(t *testing.T) {
	w := &stubbornWorker{name: "consumer", release: make(chan struct{})}
	group, err := NewGroup("orders", w)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	runner, err := NewRunner(group, RunConfig{DrainTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil on shutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the drain budget")
	}
	close(w.release) // let the abandoned instance exit
	if !runner.Abandoned() {
		t.Fatal("Abandoned() = false, want true after an exhausted drain budget")
	}
}

// TestRunnerDoesNotReportAbandonedAfterDraining keeps the flag honest: a normal
// shutdown must still allow the host to close its resources.
// TestRunnerDoesNotReportAbandonedAfterDraining 保证标记不被滥用：正常停机时宿主
// 仍应能关闭资源。
func TestRunnerDoesNotReportAbandonedAfterDraining(t *testing.T) {
	w := &politeWorker{name: "consumer"}
	group, err := NewGroup("orders", w)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	runner, err := NewRunner(group, RunConfig{DrainTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil on shutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
	if runner.Abandoned() {
		t.Fatal("Abandoned() = true, want false when every instance drained")
	}
}
