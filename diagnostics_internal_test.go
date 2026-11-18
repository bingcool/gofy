package gofy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/diagnostics"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// diagTestModule exists to give the request metrics a registered route pattern,
// which is the label the test asserts on.
// diagTestModule 用于给请求指标一个已注册的路由模式——测试断言的正是这个标签。
type diagTestModule struct{}

func (diagTestModule) Name() string { return "diag-test" }

func (diagTestModule) Register(router *support.Router) error {
	router.GET("/orders/:id", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return nil
}

// TestAppServesMetricsOnDiagnosticsPort pins the whole wiring: WithDiagnostics
// builds the pipeline, the middleware records a business request, and the scrape
// endpoint answers on the operational listener rather than the application port.
// TestAppServesMetricsOnDiagnosticsPort 固化整条接线：WithDiagnostics 构建管道、
// 中间件记录业务请求、抓取端点在运维监听而非应用端口上作答。
func TestAppServesMetricsOnDiagnosticsPort(t *testing.T) {
	httpPort := freePort(t)
	diagPort := freePort(t)

	app, err := New(
		WithName("metrics-wiring-test"),
		WithEnv("test"),
		WithHTTP(HTTPConfig{Host: "127.0.0.1", Port: httpPort, Mode: "test"}),
		WithLogger(zap.NewNop()),
		WithDiagnostics(diagnostics.Config{
			Enabled:      true,
			Addr:         fmt.Sprintf("127.0.0.1:%d", diagPort),
			DisablePprof: true,
		}),
		WithModules(diagTestModule{}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runDone := make(chan error, 1)
	go func() { runDone <- app.Run(context.Background()) }()
	waitReady(t, app)

	client := &http.Client{Timeout: 5 * time.Second}

	// One business request so the HTTP instruments have a sample.
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/diag-test/orders/7", httpPort))
	if err != nil {
		t.Fatalf("business request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("business request = %d, body=%s", resp.StatusCode, body)
	}
	_ = resp.Body.Close()

	body := get(t, client, fmt.Sprintf("http://127.0.0.1:%d/metrics", diagPort))
	for _, want := range []string{
		"process_runtime_go_goroutines",
		"http_server_request_duration",
		`http_route="/diag-test/orders/:id"`,
		`service_name="metrics-wiring-test"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scrape output is missing %q\n%s", want, body)
		}
	}

	// The application port must not serve the scrape endpoint: metrics belong to
	// the operational listener only.
	// 应用端口不得提供抓取端点：指标只属于运维监听。
	resp, err = client.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", httpPort))
	if err != nil {
		t.Fatalf("probe application port: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/metrics on the application port = %d, want 404", resp.StatusCode)
	}

	shutdownApp(t, app, runDone)
}

// TestAppExposesPprofWhenEnabled checks the other endpoint group on the same
// listener.
// TestAppExposesPprofWhenEnabled 检查同一监听上的另一组端点。
func TestAppExposesPprofWhenEnabled(t *testing.T) {
	httpPort := freePort(t)
	diagPort := freePort(t)

	app, err := New(
		WithName("pprof-wiring-test"),
		WithHTTP(HTTPConfig{Host: "127.0.0.1", Port: httpPort, Mode: "test"}),
		WithLogger(zap.NewNop()),
		WithDiagnostics(diagnostics.Config{
			Enabled:        true,
			Addr:           fmt.Sprintf("127.0.0.1:%d", diagPort),
			DisableMetrics: true,
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runDone := make(chan error, 1)
	go func() { runDone <- app.Run(context.Background()) }()
	waitReady(t, app)

	client := &http.Client{Timeout: 5 * time.Second}
	body := get(t, client, fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", diagPort))
	if !strings.Contains(body, "goroutine") {
		t.Fatalf("pprof index does not look like the standard page\n%s", body)
	}

	// Metrics were disabled, so the path must not exist.
	// 指标已关闭，因此该路径不能存在。
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", diagPort))
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/metrics with DisableMetrics = %d, want 404", resp.StatusCode)
	}

	shutdownApp(t, app, runDone)
}

// TestAppWithoutDiagnosticsOpensNoExtraPort is the default-off contract: an
// application that never asked for the endpoints must not open a socket for them.
// TestAppWithoutDiagnosticsOpensNoExtraPort 是默认关闭的契约：没要求端点的应用不得
// 为其打开 socket。
func TestAppWithoutDiagnosticsOpensNoExtraPort(t *testing.T) {
	port := freePort(t)
	app, err := New(
		WithName("no-diagnostics-test"),
		WithHTTP(HTTPConfig{Host: "127.0.0.1", Port: port, Mode: "test"}),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if app.opts.diagnostics != nil {
		t.Fatal("diagnostics config is set without WithDiagnostics")
	}

	runDone := make(chan error, 1)
	go func() { runDone <- app.Run(context.Background()) }()
	waitReady(t, app)

	if app.metrics != nil {
		t.Fatal("metrics pipeline was built without WithDiagnostics")
	}
	if app.diagnostics != nil {
		t.Fatal("diagnostics listener was built without WithDiagnostics")
	}

	shutdownApp(t, app, runDone)
}

// TestAppFailsWhenDiagnosticsPortIsTaken makes a port conflict a startup error.
// Silently continuing would leave an operator with a scrape config pointing at a
// port that answers nothing.
// TestAppFailsWhenDiagnosticsPortIsTaken 让端口冲突成为启动错误。静默继续会让运维
// 面对一个指向"什么都不应答"端口的抓取配置。
func TestAppFailsWhenDiagnosticsPortIsTaken(t *testing.T) {
	httpPort := freePort(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer func() { _ = occupied.Close() }()
	diagPort := occupied.Addr().(*net.TCPAddr).Port

	app, err := New(
		WithName("diagnostics-conflict-test"),
		WithHTTP(HTTPConfig{Host: "127.0.0.1", Port: httpPort, Mode: "test"}),
		WithLogger(zap.NewNop()),
		WithDiagnostics(diagnostics.Config{
			Enabled: true,
			Addr:    fmt.Sprintf("127.0.0.1:%d", diagPort),
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = app.Run(context.Background())
	if err == nil {
		t.Fatal("Run succeeded although the diagnostics port was taken")
	}
	if !strings.Contains(err.Error(), "diagnostics") {
		t.Fatalf("error does not name diagnostics: %v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func waitReady(t *testing.T, app *App) {
	t.Helper()
	select {
	case <-app.ready:
	case <-time.After(10 * time.Second):
		t.Fatal("application did not become ready")
	}
}

func get(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", url, resp.StatusCode)
	}
	return string(body)
}

func shutdownApp(t *testing.T, app *App, runDone chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned %v after shutdown", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}
}
