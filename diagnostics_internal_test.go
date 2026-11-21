package gofy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/diagnostics"
	"github.com/bingcool/gofy/support/health"
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

// TestAppServesHealthDetailOnDiagnosticsPort pins the Phase 1 wiring end to end:
// one registry decides /readyz on the application port, and the detail lands on
// the diagnostics listener only. The listener runs with metrics and pprof off,
// which is the health-only configuration the relaxed ErrNothingEnabled rule
// exists for.
// TestAppServesHealthDetailOnDiagnosticsPort 端到端固定 Phase 1 的接线：同一个注册表决定
// 应用端口上的 /readyz，明细只出现在诊断监听上。该监听关闭 metrics 与 pprof，正是放宽
// ErrNothingEnabled 后支持的"只开 health"配置。
func TestAppServesHealthDetailOnDiagnosticsPort(t *testing.T) {
	httpPort := freePort(t)
	diagPort := freePort(t)

	app, err := New(
		WithName("health-wiring-test"),
		WithEnv("test"),
		WithHTTP(HTTPConfig{Host: "127.0.0.1", Port: httpPort, Mode: "test"}),
		WithLogger(zap.NewNop()),
		WithHealthEndpoints(true),
		WithHealthChecks(health.Named("downstream", func(context.Context) error {
			return errors.New("downstream is down")
		})),
		WithDiagnostics(diagnostics.Config{
			Enabled:        true,
			Addr:           fmt.Sprintf("127.0.0.1:%d", diagPort),
			DisablePprof:   true,
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
	probe := func(url string) (int, string) {
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
		return resp.StatusCode, string(body)
	}

	appBase := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	if code, _ := probe(appBase + "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", code)
	}
	if code, body := probe(appBase + "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d (%s), want 503 from the failing check", code, body)
	}
	// The detail belongs to the operational listener, never the public port.
	// 明细属于运维监听，绝不放到业务端口上。
	if code, _ := probe(appBase + "/health"); code != http.StatusNotFound {
		t.Fatalf("/health on the application port = %d, want 404", code)
	}

	code, body := probe(fmt.Sprintf("http://127.0.0.1:%d/health", diagPort))
	if code != http.StatusOK {
		t.Fatalf("diagnostics /health = %d, want 200", code)
	}
	for _, want := range []string{`"status":"unavailable"`, `"name":"downstream"`, `"class":"readiness"`, `"status":"failed"`, "downstream is down"} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail body is missing %q\n%s", want, body)
		}
	}

	shutdownApp(t, app, runDone)
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

// slowRequestModule keeps one business request in flight while shutdown
// starts, so the drain path is observable.
// slowRequestModule 让一个业务请求在停机开始时仍在途，从而能观察排空路径。
type slowRequestModule struct {
	started chan struct{}
	release chan struct{}
}

func (slowRequestModule) Name() string { return "slow-request" }

func (m slowRequestModule) Register(router *support.Router) error {
	router.GET("/slow", func(c *gin.Context) {
		select {
		case m.started <- struct{}{}:
		default:
		}
		<-m.release
		c.String(http.StatusOK, "drained")
	})
	return nil
}

// TestDiagnosticsShutdownKeepsTheBusinessBudget pins the budget split: an
// operational request that outlives its own short budget must not consume the
// time the main server uses to drain in-flight business requests. Before the
// split, one 30s pprof profile ate the whole shutdown timeout, srv.Shutdown
// returned with an expired context and srv.Close cut the drain short.
// TestDiagnosticsShutdownKeepsTheBusinessBudget 固定预算划分：活得比自身短预算更久的
// 运维请求，不能吃掉主服务用于排空在途业务请求的时间。拆分之前，一个 30s 的 pprof
// profile 会耗尽整个停机超时，srv.Shutdown 拿到过期 ctx 后 srv.Close 把排空截断。
func TestDiagnosticsShutdownKeepsTheBusinessBudget(t *testing.T) {
	httpPort := freePort(t)
	diagPort := freePort(t)
	mod := slowRequestModule{started: make(chan struct{}, 1), release: make(chan struct{})}

	app, err := New(
		WithName("diagnostics-budget-test"),
		WithEnv("test"),
		WithHTTP(HTTPConfig{Host: "127.0.0.1", Port: httpPort, Mode: "test"}),
		WithLogger(zap.NewNop()),
		WithShutdownTimeout(5*time.Second),
		WithDiagnostics(diagnostics.Config{
			Enabled: true,
			Addr:    fmt.Sprintf("127.0.0.1:%d", diagPort),
		}),
		WithModules(mod),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runDone := make(chan error, 1)
	go func() { runDone <- app.Run(context.Background()) }()
	waitReady(t, app)

	client := &http.Client{Timeout: 30 * time.Second}

	// One business request that stays in flight across the whole shutdown.
	// 一个横跨整个停机过程的在途业务请求。
	slowDone := make(chan error, 1)
	go func() {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/slow-request/slow", httpPort))
		if err != nil {
			slowDone <- err
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			slowDone <- fmt.Errorf("slow request = %d, body=%s", resp.StatusCode, body)
			return
		}
		slowDone <- nil
	}()
	select {
	case <-mod.started:
	case <-time.After(10 * time.Second):
		t.Fatal("slow handler never started")
	}

	// A pprof profile that outlives the diagnostics budget by far.
	// 一个远超诊断预算的 pprof profile。
	profileDone := make(chan struct{})
	go func() {
		defer close(profileDone)
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/profile?seconds=10", diagPort))
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	// Give the profile time to reach the handler; a request that has not
	// arrived yet would be refused instantly when the listener closes, and the
	// test would prove nothing.
	// 给 profile 一点时间进入 handler：还没到达的请求会在监听关闭时立刻被拒，
	// 那样测试什么也证明不了。
	time.Sleep(300 * time.Millisecond)

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- app.Shutdown(context.Background()) }()

	// The diagnostics listener closes once its own budget runs out and cuts
	// the profile request; only then is the business request released, which
	// is what tells the two implementations apart.
	// 诊断监听在自己的预算耗尽后关闭并切断 profile 请求；这时才放行业务请求，
	// 两种实现由此区分。
	select {
	case <-profileDone:
	case <-time.After(20 * time.Second):
		t.Fatal("diagnostics listener never closed its stalled request")
	}
	close(mod.release)

	select {
	case err := <-slowDone:
		if err != nil {
			t.Fatalf("in-flight request was not drained: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight request never finished")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after Shutdown")
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
