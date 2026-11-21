package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/otel"
)

// readinessMetricWorker behaves like a consumer: Run reports readiness only
// after its "connection" is up, which is what the gauges are meant to describe.
// readinessMetricWorker 像消费者一样工作：Run 在"连接"建好后才上报就绪，正是 gauge
// 要描述的时刻。
type readinessMetricWorker struct {
	name string
}

func (w *readinessMetricWorker) Name() string  { return w.name }
func (w *readinessMetricWorker) ReportsReady() {}
func (w *readinessMetricWorker) Run(ctx context.Context) error {
	Ready(ctx)
	<-ctx.Done()
	return nil
}

func TestRunnerEmitsLifecycleMetrics(t *testing.T) {
	metrics, err := otel.NewMetrics(otel.MetricsConfig{Enabled: true}, otel.WithServiceName("worker-metrics-test"))
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = metrics.Shutdown(context.Background()) }()

	worker := &readinessMetricWorker{name: "consumer"}
	group, err := NewGroup("orders", worker)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	runner, err := NewRunner(group, RunConfig{
		MeterProvider: metrics.MeterProvider(),
		Metrics:       true,
		Concurrency:   map[string]int{"consumer": 2},
		DrainTimeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	waitForRunnerReady(t, runner)

	body := scrapeWorkerMetrics(t, metrics)
	labels := []string{`group="orders"`, `name="consumer"`}
	for _, want := range []struct {
		name  string
		value string
	}{
		{"gofy_worker_ready", "1"},
		{"gofy_worker_instances_ready", "2"},
		{"gofy_worker_instances_total", "2"},
	} {
		line := workerMetricLine(body, want.name, labels...)
		if !strings.HasSuffix(line, " "+want.value) {
			t.Fatalf("%s = %q, want %s\n%s", want.name, line, want.value, body)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop")
	}

	body = scrapeWorkerMetrics(t, metrics)
	if line := workerMetricLine(body, "gofy_worker_run_duration_seconds_count", `group="orders"`, `name="consumer"`, `outcome="ok"`); line == "" {
		t.Fatalf("run duration histogram is missing\n%s", body)
	}
	if line := workerMetricLine(body, "gofy_worker_ready", labels...); !strings.HasSuffix(line, " 0") {
		t.Fatalf("readiness gauge did not reset on shutdown: %q\n%s", line, body)
	}
}

// restartAndTickWorker fails once to force a restart, then runs a failing ticker
// so two independent instruments can be checked in one lifecycle.
// restartAndTickWorker 先失败一次以触发重启，随后运行一个失败的 ticker，
// 于是一次生命周期里能检查两个独立 instrument。
type restartAndTickWorker struct {
	name  string
	calls atomic.Int32
	ticks atomic.Int32
}

func (w *restartAndTickWorker) Name() string { return w.name }
func (w *restartAndTickWorker) Run(ctx context.Context) error {
	if w.calls.Add(1) == 1 {
		return errors.New("boom")
	}
	return Ticker(ctx, 2, func(context.Context) error {
		w.ticks.Add(1)
		return errors.New("tick failed")
	})
}

func TestRunnerEmitsRestartAndTickMetrics(t *testing.T) {
	metrics, err := otel.NewMetrics(otel.MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = metrics.Shutdown(context.Background()) }()

	worker := &restartAndTickWorker{name: "poller"}
	group, err := NewGroup("orders", worker)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	runner, err := NewRunner(group, RunConfig{
		MeterProvider: metrics.MeterProvider(),
		Metrics:       true,
		Policy:        Policy{Backoff: time.Millisecond, MaxBackoff: time.Millisecond},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for worker.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if worker.calls.Load() < 2 {
		t.Fatal("worker was not restarted")
	}
	deadline = time.Now().Add(5 * time.Second)
	for worker.ticks.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if worker.ticks.Load() == 0 {
		t.Fatal("ticker never ran")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop")
	}

	body := scrapeWorkerMetrics(t, metrics)
	labels := []string{`group="orders"`, `name="poller"`}
	if line := workerMetricLine(body, "gofy_worker_restarts_total", labels...); !strings.HasSuffix(line, " 1") {
		t.Fatalf("restarts = %q, want 1\n%s", line, body)
	}
	for _, name := range []string{"gofy_worker_tick_errors_total", "gofy_worker_tick_duration_seconds_count"} {
		if line := workerMetricLine(body, name, labels...); line == "" {
			t.Fatalf("scrape is missing %s\n%s", name, body)
		}
	}
}

func TestRunnerEmitsAbandonedMetric(t *testing.T) {
	metrics, err := otel.NewMetrics(otel.MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = metrics.Shutdown(context.Background()) }()

	worker := &stubbornWorker{name: "consumer", release: make(chan struct{})}
	group, err := NewGroup("orders", worker)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	runner, err := NewRunner(group, RunConfig{
		MeterProvider: metrics.MeterProvider(),
		Metrics:       true,
		DrainTimeout:  50 * time.Millisecond,
	})
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
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not return after the drain budget")
	}
	close(worker.release)

	body := scrapeWorkerMetrics(t, metrics)
	if line := workerMetricLine(body, "gofy_worker_abandoned_total", `group="orders"`); !strings.HasSuffix(line, " 1") {
		t.Fatalf("abandoned = %q, want 1\n%s", line, body)
	}
}

// TestDisabledWorkerMetricsDoNotAllocate is the acceptance rule for a process
// with metrics off: no attributes are built and no instrument is touched.
// TestDisabledWorkerMetricsDoNotAllocate 是没有指标的进程必须满足的验收规则：
// 不构造标签，也不触碰任何 instrument。
func TestDisabledWorkerMetricsDoNotAllocate(t *testing.T) {
	m := &workerMetrics{group: "orders", names: []string{"consumer"}}
	id := Identity{Group: "orders", Name: "consumer"}
	totals := map[string]int{"consumer": 2}
	ctx := context.Background()
	allocs := testing.AllocsPerRun(200, func() {
		m.setReady(1)
		m.setInstances(totals, nil)
		m.recordRestart(id)
		m.recordRun(id, time.Second, false)
		m.recordAbandoned()
		m.recordTick(ctx, time.Second, true)
	})
	if allocs != 0 {
		t.Fatalf("disabled metrics allocated %g times per call", allocs)
	}
}

func scrapeWorkerMetrics(t *testing.T, m *otel.Metrics) string {
	t.Helper()
	handler := m.Handler()
	if handler == nil {
		t.Fatal("metrics handler is nil")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, otel.DefaultMetricsPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// workerMetricLine finds a sample by name and label values. The exporter adds
// otel_scope_* labels, so an exact string match would bind the test to exporter
// internals rather than to the metric contract.
// workerMetricLine 按名字与标签值找样本。exporter 会补 otel_scope_* 标签，
// 精确字符串匹配会把测试绑到 exporter 实现，而不是指标契约。
func workerMetricLine(body, name string, labels ...string) string {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+"{") {
			continue
		}
		matched := true
		for _, label := range labels {
			if !strings.Contains(line, label) {
				matched = false
				break
			}
		}
		if matched {
			return line
		}
	}
	return ""
}

func waitForRunnerReady(t *testing.T, runner *Runner) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !runner.IsReady() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !runner.IsReady() {
		t.Fatal("runner never became ready")
	}
}
