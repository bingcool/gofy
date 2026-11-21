package gofy_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support/diagnostics"
	"github.com/bingcool/gofy/support/otel"
	"github.com/bingcool/gofy/worker"
)

// TestWorkerServesMetricsEndpoint is the Phase 2 acceptance test at the process
// level: building a MeterProvider alone is not enough, a worker must expose a
// scrape endpoint with its own gofy_worker_* series.
// TestWorkerServesMetricsEndpoint 是 Phase 2 的进程级验收：只构建 MeterProvider
// 不够，worker 必须提供抓取端点并暴露自己的 gofy_worker_* 序列。
func TestWorkerServesMetricsEndpoint(t *testing.T) {
	diagPort := freePort(t)
	diagAddr := fmt.Sprintf("127.0.0.1:%d", diagPort)
	started := make(chan struct{})
	var once sync.Once
	group, err := worker.NewGroup("orders", worker.Func("tick", 1, func(ctx context.Context) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return nil
	}))
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	app, err := gofy.New(
		gofy.WithName("worker-metrics-host"),
		gofy.WithWorkerGroups(group),
		gofy.WithDiagnostics(diagnostics.Config{
			Enabled:       true,
			Addr:          diagAddr,
			DisablePprof:  true,
			DisableHealth: true,
		}),
		gofy.WithMetrics(otel.MetricsConfig{Enabled: true}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.ExecuteArgs(ctx, []string{"worker", "--group=orders"}) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + diagAddr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read /metrics: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200: %s", resp.StatusCode, raw)
	}
	body := string(raw)
	for _, want := range []string{
		`gofy_worker_ready{group="orders"`,
		`gofy_worker_instances_total{group="orders"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("worker /metrics is missing %q\n%s", want, body)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ExecuteArgs: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker command did not stop")
	}
}
