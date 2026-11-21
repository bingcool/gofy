package gofy_test

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/worker"
)

// slowStartingWorker is a worker that connects for a while before it can accept
// work, which is what the four message consumers do inside Run.
type slowStartingWorker struct {
	name string

	// connect is closed by the test to let Run finish its "connection".
	connect chan struct{}
	// connected is closed by Run right after it reports readiness.
	connected chan struct{}
	// release keeps Run inside the drain window after the context is cancelled,
	// so the probe's 503 during shutdown is observable.
	release chan struct{}

	started atomic.Int32
}

func (w *slowStartingWorker) Name() string       { return w.name }
func (w *slowStartingWorker) Concurrency() uint8 { return 1 }
func (w *slowStartingWorker) ReportsReady()      {}

func (w *slowStartingWorker) Run(ctx context.Context) error {
	w.started.Add(1)
	select {
	case <-w.connect:
		// This is the moment a consumer has its connection and subscription.
		// 这一刻相当于消费者已经建好连接与订阅。
		worker.Ready(ctx)
		close(w.connected)
	case <-ctx.Done():
		return nil
	}
	<-ctx.Done()
	<-w.release
	return nil
}

// workerProbeCode reads one probe endpoint. Liveness and readiness answer
// different questions now, so the caller has to name the path it is asking
// about.
// workerProbeCode 读取一个探针端点。liveness 与 readiness 现在回答不同的问题，调用方
// 必须指明问的是哪一个。
func workerProbeCode(t *testing.T, port int, path string) int {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// TestWorkerProbeWaitsForRealStartup is P1-04's acceptance test at the host
// level: the probe stays 503 while the worker is still connecting, turns 200
// only after every instance reported, and returns to 503 as soon as the
// shutdown starts instead of after the drain finishes.
// TestWorkerProbeWaitsForRealStartup 是 P1-04 在宿主层的验收测试：worker 还在建连
// 时探针保持 503，只有全部实例上报后才变 200；停机一开始立刻回到 503，而不是等
// drain 结束。
func TestWorkerProbeWaitsForRealStartup(t *testing.T) {
	healthPort := freePort(t)
	healthAddr := "127.0.0.1:" + strconv.Itoa(healthPort)

	w := &slowStartingWorker{
		name:      "consumer",
		connect:   make(chan struct{}),
		connected: make(chan struct{}),
		release:   make(chan struct{}),
	}
	group, err := worker.NewGroup("orders", w)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	app, err := gofy.New(
		gofy.WithName("worker-ready-test"),
		gofy.WithWorkerGroups(group),
		gofy.WithWorkerHealth(healthAddr),
		gofy.WithWorkerDrainTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.ExecuteArgs(ctx, []string{"worker", "--group=orders"}) }()

	waitFor(t, func() bool { return w.started.Load() == 1 }, "the worker to start")
	// The probe must answer 503 while Run has not finished connecting: a
	// scheduler that saw 200 here would send work to a consumer with no
	// subscription.
	// Run 还没建完连接时探针必须回 503：如果这时报 200，调度器会把活派给一个尚未
	// 订阅的消费者。
	waitFor(t, func() bool { return workerProbeCode(t, healthPort, "/readyz") == http.StatusServiceUnavailable },
		"readiness to report 503 before the worker connects")
	// Liveness answers while the process is starting: a scheduler that sees a
	// restart here would kill a process that is still connecting.
	// 启动期间 liveness 就要应答：这里若失败，调度器会重启一个仍在建连的进程。
	if got := workerProbeCode(t, healthPort, "/healthz"); got != http.StatusOK {
		t.Fatalf("liveness before the worker connects = %d, want 200", got)
	}

	close(w.connect)
	select {
	case <-w.connected:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not report readiness")
	}
	waitFor(t, func() bool { return workerProbeCode(t, healthPort, "/readyz") == http.StatusOK },
		"readiness to report 200 after the worker connected")

	cancel()
	// Drain starts now. The worker is still inside Run waiting for release, so
	// a probe that only flipped after the last runner returned would keep
	// answering 200 through the whole window.
	// drain 从此刻开始。worker 仍停在 Run 里等待 release，因此"等最后一个 runner
	// 返回才转 503"的探针会在整个窗口里继续回 200。
	waitFor(t, func() bool { return workerProbeCode(t, healthPort, "/readyz") == http.StatusServiceUnavailable },
		"readiness to report 503 as soon as the drain starts")
	// Liveness stays green through the drain window: restarting a process that
	// is shutting down cleanly turns a rollout into a crash loop.
	// drain 窗口内 liveness 保持绿色：重启一个正在干净停机的进程会把滚动发布变成崩溃循环。
	if got := workerProbeCode(t, healthPort, "/healthz"); got != http.StatusOK {
		t.Fatalf("liveness during the drain = %d, want 200", got)
	}

	close(w.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker command: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker command did not stop")
	}
}
