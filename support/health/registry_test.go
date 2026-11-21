package health

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustRegister(t *testing.T, reg *Registry, name string, class Class, fn CheckFunc) {
	t.Helper()
	if err := reg.Register(Named(name, fn), WithClass(class)); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
}

// TestRegistryEmptyIsReady pins the zero-check case: a process with nothing to
// check is ready, otherwise enabling the endpoints without checks would drain it.
// TestRegistryEmptyIsReady 固定零检查的情形：没有检查可做的进程就是就绪的，否则开启端点
// 但不注册检查会把进程摘流。
func TestRegistryEmptyIsReady(t *testing.T) {
	reg := New(Options{})
	snap := reg.Ready(context.Background())
	if !snap.Ready {
		t.Fatal("empty registry reported not ready")
	}
	if len(snap.Results) != 0 {
		t.Fatalf("results = %d, want 0", len(snap.Results))
	}
}

// TestRegistryRejectsInvalidChecks pins the construction contract: every rejected
// input would either panic later or silently shadow a check that already answers.
// TestRegistryRejectsInvalidChecks 固定构造期约定：被拒绝的每一种输入要么之后 panic，
// 要么静默遮蔽已在应答的检查。
func TestRegistryRejectsInvalidChecks(t *testing.T) {
	reg := New(Options{})
	ok := func(context.Context) error { return nil }

	if err := reg.Register(nil); err == nil {
		t.Fatal("Register accepted a nil checker")
	}
	if err := reg.Register(Named("  ", ok)); err == nil {
		t.Fatal("Register accepted an empty name")
	}
	if err := reg.Register(Named("dup", ok)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(Named("dup", ok)); err == nil {
		t.Fatal("Register accepted a duplicate name")
	}
	if err := reg.Register(Named("class", ok), WithClass(Class("weird"))); err == nil {
		t.Fatal("Register accepted an unknown class")
	}
	if err := reg.Register(Named("neg", ok), WithCheckTimeout(-time.Second)); err == nil {
		t.Fatal("Register accepted a negative timeout")
	}
}

// TestRegistryReadinessIsFailClosed pins the traffic decision: a readiness
// failure reports not ready, and a check that never answered counts the same.
// TestRegistryReadinessIsFailClosed 固定接活结论：readiness 失败即报告未就绪，没有给出
// 结论的检查同样按未就绪计。
func TestRegistryReadinessIsFailClosed(t *testing.T) {
	reg := New(Options{ReadinessTTL: -1, DiagnosticTTL: -1})
	mustRegister(t, reg, "database", ClassReadiness, func(context.Context) error {
		return errors.New("down")
	})

	snap := reg.Ready(context.Background())
	if snap.Ready {
		t.Fatal("failing readiness check reported ready")
	}
	if got := snap.Failed(); len(got) != 1 || got[0] != "database" {
		t.Fatalf("Failed() = %v, want [database]", got)
	}
}

// TestRegistryDiagnosticFailureDoesNotBlockReady pins the class boundary: a
// diagnostic check is detail, never a traffic decision.
// TestRegistryDiagnosticFailureDoesNotBlockReady 固定类别边界：诊断检查只提供明细，
// 从不参与接活决策。
func TestRegistryDiagnosticFailureDoesNotBlockReady(t *testing.T) {
	reg := New(Options{ReadinessTTL: -1, DiagnosticTTL: -1})
	mustRegister(t, reg, "report", ClassDiagnostic, func(context.Context) error {
		return errors.New("down")
	})

	if !reg.Ready(context.Background()).Ready {
		t.Fatal("diagnostic failure blocked readiness")
	}

	snap := reg.Diagnose(context.Background())
	if len(snap.Results) != 1 {
		t.Fatalf("diagnose results = %d, want 1", len(snap.Results))
	}
	if got := snap.Results[0].Status; got != StatusUnknown {
		t.Fatalf("diagnostic status = %s, want unknown", got)
	}
}

// TestRegistryCachesWithinTTL pins the TTL: repeated probes inside one window
// share a single round, which is what keeps N replicas from multiplying M
// dependencies by the probe frequency.
// TestRegistryCachesWithinTTL 固定 TTL：同一窗口内的重复探测共享一轮，这正是避免
// N 个副本把 M 个依赖乘以探测频率的原因。
func TestRegistryCachesWithinTTL(t *testing.T) {
	var calls int32
	reg := New(Options{ReadinessTTL: time.Hour})

	if err := reg.Register(Named("counted", func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})); err != nil {
		t.Fatalf("register: %v", err)
	}

	for i := 0; i < 5; i++ {
		if !reg.Ready(context.Background()).Ready {
			t.Fatalf("round %d reported not ready", i)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("checks run = %d, want 1 within one TTL", got)
	}

	reg.Invalidate()
	if !reg.Ready(context.Background()).Ready {
		t.Fatal("post-invalidate round reported not ready")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("checks run after invalidate = %d, want 2", got)
	}
}

// TestRegistrySingleflightMergesConcurrentProbes pins the merge: 100 probes
// arriving together trigger one round, not 100.
// TestRegistrySingleflightMergesConcurrentProbes 固定合并：同时到达的 100 个探针只触发
// 一轮，而不是 100 轮。
func TestRegistrySingleflightMergesConcurrentProbes(t *testing.T) {
	var calls int32
	release := make(chan struct{})

	reg := New(Options{ReadinessTTL: time.Hour})
	if err := reg.Register(Named("slow", func(ctx context.Context) error {
		atomic.AddInt32(&calls, 1)
		<-release
		return nil
	})); err != nil {
		t.Fatalf("register: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg.Ready(context.Background())
		}()
	}

	// Wait for the single round to enter the check, then let it finish.
	// 等这一轮进入检查函数，再放行。
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("checks run = %d, want 1", got)
	}
}

// TestRegistryPanicIsIsolated pins panic recovery: one panicking check fails
// alone and does not abort the round.
// TestRegistryPanicIsIsolated 固定 panic 隔离：一个 panic 的检查只失败自己，不中断整轮。
func TestRegistryPanicIsIsolated(t *testing.T) {
	reg := New(Options{ReadinessTTL: -1})
	mustRegister(t, reg, "boom", ClassReadiness, func(context.Context) error {
		panic("kaboom")
	})
	mustRegister(t, reg, "fine", ClassReadiness, func(context.Context) error { return nil })

	snap := reg.Ready(context.Background())
	if len(snap.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(snap.Results))
	}
	for _, res := range snap.Results {
		if res.Name == "boom" {
			if res.Status != StatusFailed || res.Reason != ReasonPanic {
				t.Fatalf("panicking check = %s/%s, want failed/panic", res.Status, res.Reason)
			}
		}
		if res.Name == "fine" && res.Status != StatusHealthy {
			t.Fatalf("healthy check = %s, want healthy", res.Status)
		}
	}
	if snap.Ready {
		t.Fatal("panicking readiness check reported ready")
	}
}

// TestRegistryCheckTimeoutIsReported pins the per-check timeout reason, so an
// operator can tell a slow dependency apart from a broken one.
// TestRegistryCheckTimeoutIsReported 固定单检查超时的原因串，运维因此能把慢依赖与坏依赖
// 区分开。
func TestRegistryCheckTimeoutIsReported(t *testing.T) {
	reg := New(Options{ReadinessTTL: -1, Timeout: 20 * time.Millisecond})
	mustRegister(t, reg, "slow", ClassReadiness, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	snap := reg.Ready(context.Background())
	if len(snap.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(snap.Results))
	}
	res := snap.Results[0]
	if res.Status != StatusFailed || res.Reason != ReasonTimeout {
		t.Fatalf("timeout result = %s/%s, want failed/timeout", res.Status, res.Reason)
	}
}

// TestRegistryBudgetMarksUnfinishedChecks pins the boundary the design review
// called out: with one slot and a budget that expires first, a check that never
// got to answer must be reported as budget, not as healthy.
//
// Both checks block on the round context, so the outcome does not depend on
// which goroutine wins the only slot: the winner is cancelled by the budget and
// the other either never starts or starts with an expired context. Both are
// "no answer", and both must fail closed.
// TestRegistryBudgetMarksUnfinishedChecks 固定设计复核对指出的边界：一个槽位、预算先到期
// 时，没来得及给出结论的检查必须报告为 budget，而不是健康。
//
// 两个检查都阻塞在整轮 context 上，因此结论不依赖哪个 goroutine 抢到唯一槽位：抢到的被
// 预算取消，另一个要么没开始，要么拿着已过期的 context 开始。两者都是"没有结论"，都必须
// fail-closed。
func TestRegistryBudgetMarksUnfinishedChecks(t *testing.T) {
	reg := New(Options{
		ReadinessTTL: -1,
		Concurrency:  1,
		Timeout:      200 * time.Millisecond,
		TotalTimeout: 30 * time.Millisecond,
	})
	block := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	mustRegister(t, reg, "first", ClassReadiness, block)
	mustRegister(t, reg, "second", ClassReadiness, block)

	snap := reg.Ready(context.Background())
	if len(snap.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(snap.Results))
	}
	for _, res := range snap.Results {
		if res.Status != StatusFailed || res.Reason != ReasonBudget {
			t.Fatalf("check %q = %s/%s, want failed/budget", res.Name, res.Status, res.Reason)
		}
	}
	if snap.Ready {
		t.Fatal("budget-exhausted round reported ready")
	}
}

// TestRegistryTimeoutClassMapping pins that a diagnostic check which times out is
// unknown while a readiness check is failed: the same timeout means different
// things for traffic.
// TestRegistryTimeoutClassMapping 固定类别映射：诊断检查超时记 unknown，readiness 检查
// 超时记 failed——同一个超时对流量有不同含义。
func TestRegistryTimeoutClassMapping(t *testing.T) {
	reg := New(Options{ReadinessTTL: -1, DiagnosticTTL: -1, Timeout: 20 * time.Millisecond})
	mustRegister(t, reg, "diag", ClassDiagnostic, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	snap := reg.Diagnose(context.Background())
	if len(snap.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(snap.Results))
	}
	if got := snap.Results[0].Status; got != StatusUnknown {
		t.Fatalf("diagnostic timeout status = %s, want unknown", got)
	}
	if !snap.Ready {
		t.Fatal("diagnostic timeout blocked readiness")
	}
}

// negativeTimeoutChecker declares a timeout below zero, which is a bug in the
// checker rather than a request for the default.
// negativeTimeoutChecker 声明负超时，这是检查器的 bug，而不是"使用默认值"的请求。
type negativeTimeoutChecker struct{}

func (negativeTimeoutChecker) Name() string                { return "neg-provider" }
func (negativeTimeoutChecker) Check(context.Context) error { return nil }
func (negativeTimeoutChecker) CheckTimeout() time.Duration { return -time.Second }

// TestRegistryRejectsNegativeProviderTimeout pins the ordering: the provider is
// consulted first and its value is validated too, otherwise a checker could
// silently run under the default budget it did not ask for.
// TestRegistryRejectsNegativeProviderTimeout 固定顺序：先查询 provider，再校验它的值，
// 否则检查器会静默运行在并非它要求的默认预算下。
func TestRegistryRejectsNegativeProviderTimeout(t *testing.T) {
	reg := New(Options{})
	if err := reg.Register(negativeTimeoutChecker{}); err == nil {
		t.Fatal("Register accepted a checker whose CheckTimeout is negative")
	}
}
