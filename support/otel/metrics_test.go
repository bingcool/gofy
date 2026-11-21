package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

func TestMetricsDisabledIsInert(t *testing.T) {
	m, err := NewMetrics(MetricsConfig{})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	if m.Enabled() {
		t.Fatal("metrics reported enabled for a disabled config")
	}
	if m.Handler() != nil {
		t.Fatal("Handler returned non-nil for a disabled config")
	}
	if mw := m.GinMiddleware(nil); mw != nil {
		t.Fatal("GinMiddleware returned non-nil for a disabled config")
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown on disabled metrics: %v", err)
	}
	if m.Path() != DefaultMetricsPath {
		t.Fatalf("Path = %q, want %q", m.Path(), DefaultMetricsPath)
	}
}

func TestMetricsDisabledIsSafeThroughNilPointer(t *testing.T) {
	var m *Metrics
	if m.Enabled() {
		t.Fatal("nil Metrics reported enabled")
	}
	if m.Handler() != nil {
		t.Fatal("nil Metrics returned a handler")
	}
	if mw := m.GinMiddleware(nil); mw != nil {
		t.Fatal("nil Metrics returned middleware")
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown on nil metrics: %v", err)
	}
}

func TestNewMetricsRejectsBadPath(t *testing.T) {
	for _, path := range []string{"metrics", "/met rics", "/metrics\tx"} {
		if _, err := NewMetrics(MetricsConfig{Enabled: true, Path: path}); err == nil {
			t.Fatalf("NewMetrics accepted path %q", path)
		}
	}
	// Surrounding spaces are trimmed rather than rejected, matching how every
	// other gofy config field treats them.
	// 首尾空格会被去掉而不是报错，与 gofy 其他配置字段的做法一致。
	if _, err := NewMetrics(MetricsConfig{Enabled: true, Path: " /metrics "}); err != nil {
		t.Fatalf("NewMetrics rejected a padded but valid path: %v", err)
	}
}

func TestMetricsExposesRuntimeAndHTTPInstruments(t *testing.T) {
	m, err := NewMetrics(MetricsConfig{Enabled: true},
		WithServiceName("metrics-test"),
		WithServiceVersion("v1"),
		WithEnvironment("test"),
	)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = m.Shutdown(context.Background()) }()

	if !m.Enabled() {
		t.Fatal("metrics not enabled")
	}
	if m.Path() != DefaultMetricsPath {
		t.Fatalf("Path = %q, want %q", m.Path(), DefaultMetricsPath)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(m.GinMiddleware(nil))
	engine.GET("/users/:id", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/42", nil))

	body := scrape(t, m)

	for _, want := range []string{
		"process_runtime_go_goroutines",
		"process_runtime_go_mem_heap_alloc",
		"process_runtime_go_mem_heap_objects",
		"process_runtime_go_gc_count",
		"http_server_request_duration",
		"http_server_active_requests",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scrape output is missing %q\n%s", want, body)
		}
	}
}

// TestMetricsUsesRoutePatternNotPath is the cardinality guard: a path parameter
// must never become a label value, or every user id creates a time series.
// TestMetricsUsesRoutePatternNotPath 是基数守卫：路径参数绝不能变成标签值，否则每个
// 用户 id 都会造出一条时间线。
func TestMetricsUsesRoutePatternNotPath(t *testing.T) {
	m, err := NewMetrics(MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = m.Shutdown(context.Background()) }()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(m.GinMiddleware(nil))
	engine.GET("/users/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.POST("/users/:id", func(c *gin.Context) { c.Status(http.StatusCreated) })

	for _, path := range []string{"/users/1", "/users/2", "/users/999"} {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	}
	// An unmatched request must collapse into a single series instead of
	// carrying the raw path.
	// 未匹配的请求必须收敛为一条序列，而不是带上原始路径。
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope/123", nil))

	body := scrape(t, m)
	if !strings.Contains(body, `http_route="/users/:id"`) {
		t.Fatalf("route label is missing the registered pattern\n%s", body)
	}
	if strings.Contains(body, "/users/999") || strings.Contains(body, "/users/1") {
		t.Fatalf("route label leaked a concrete path\n%s", body)
	}
	if !strings.Contains(body, `http_route="unmatched"`) {
		t.Fatalf("unmatched requests were not bucketed\n%s", body)
	}
	if !strings.Contains(body, `http_response_status_code="404"`) {
		t.Fatalf("status code label is missing\n%s", body)
	}
}

// TestMetricsNormalizesUnknownMethod keeps an invented verb from creating its own
// series; net/http accepts any token as the method.
// TestMetricsNormalizesUnknownMethod 阻止自造动词单独成线；net/http 接受任意 token
// 作为方法。
func TestMetricsNormalizesUnknownMethod(t *testing.T) {
	m, err := NewMetrics(MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = m.Shutdown(context.Background()) }()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(m.GinMiddleware(nil))
	engine.Any("/anything", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest("PURGE", "/anything", nil))

	body := scrape(t, m)
	if !strings.Contains(body, `http_request_method="OTHER"`) {
		t.Fatalf("non-standard method was not normalized\n%s", body)
	}
}

func TestMetricsSkipFilterExcludesProbes(t *testing.T) {
	m, err := NewMetrics(MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = m.Shutdown(context.Background()) }()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(m.GinMiddleware(SkipPaths("/healthz")))
	engine.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET("/orders", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	body := scrape(t, m)
	if strings.Contains(body, `http_route="/healthz"`) {
		t.Fatalf("skipped probe still produced a series\n%s", body)
	}

	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders", nil))
	if body = scrape(t, m); !strings.Contains(body, `http_route="/orders"`) {
		t.Fatalf("recorded route missing after a skipped request\n%s", body)
	}
}

// TestMetricsInstancesAreIsolated covers the registry choice: a shared
// prometheus.DefaultRegisterer would make the second instance fail with a
// duplicate-registration error.
// TestMetricsInstancesAreIsolated 覆盖 registry 的选择：共用
// prometheus.DefaultRegisterer 会让第二个实例因重复注册而失败。
func TestMetricsInstancesAreIsolated(t *testing.T) {
	first, err := NewMetrics(MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("first NewMetrics: %v", err)
	}
	defer func() { _ = first.Shutdown(context.Background()) }()

	second, err := NewMetrics(MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("second NewMetrics: %v", err)
	}
	defer func() { _ = second.Shutdown(context.Background()) }()

	if !first.Enabled() || !second.Enabled() {
		t.Fatal("an instance reported disabled")
	}
	// Each scrape must succeed on its own registry.
	_ = scrape(t, first)
	_ = scrape(t, second)
}

func TestMetricsShutdownIsIdempotent(t *testing.T) {
	m, err := NewMetrics(MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := m.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown call %d: %v", i, err)
		}
	}
}

// TestMeterProviderDisabledIsNoop is the contract the worker host relies on:
// it can ask every Metrics for a provider without checking whether the pipeline
// exists. A nil receiver behaves the same way.
// TestMeterProviderDisabledIsNoop 是 worker 宿主依赖的契约：它可以无条件向任意
// Metrics 要 provider，不必判断管道是否存在；nil 接收者的行为也一样。
func TestMeterProviderDisabledIsNoop(t *testing.T) {
	disabled, err := NewMetrics(MetricsConfig{})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	var nilMetrics *Metrics
	for name, m := range map[string]*Metrics{"disabled": disabled, "nil": nilMetrics} {
		provider := m.MeterProvider()
		if provider == nil {
			t.Fatalf("%s: MeterProvider returned nil", name)
		}
		if _, ok := provider.(metricnoop.MeterProvider); !ok {
			t.Fatalf("%s: MeterProvider = %T, want noop", name, provider)
		}
		counter, err := provider.Meter("test").Int64Counter("gofy.test.counter")
		if err != nil {
			t.Fatalf("%s: noop meter rejected an instrument: %v", name, err)
		}
		counter.Add(context.Background(), 1)
	}

	enabled, err := NewMetrics(MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewMetrics(enabled): %v", err)
	}
	defer func() { _ = enabled.Shutdown(context.Background()) }()
	if _, ok := enabled.MeterProvider().(metricnoop.MeterProvider); ok {
		t.Fatal("enabled Metrics returned a noop MeterProvider")
	}
}

// TestDurationHistogramsUseSharedBuckets guards the dashboard contract for
// instruments created outside this package: a worker histogram with unit "s"
// must get the same boundaries as the HTTP one, not the SDK defaults.
// TestDurationHistogramsUseSharedBuckets 守住跨包创建的 instrument 的看板契约：
// 单位为 s 的 worker 直方图必须与 HTTP 用同一组边界，而不是 SDK 默认值。
func TestDurationHistogramsUseSharedBuckets(t *testing.T) {
	m, err := NewMetrics(MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = m.Shutdown(context.Background()) }()

	histogram, err := m.MeterProvider().Meter("test").Float64Histogram(
		"gofy.worker.run.duration", metric.WithUnit("s"))
	if err != nil {
		t.Fatalf("create histogram: %v", err)
	}
	histogram.Record(context.Background(), 0.02)

	body := scrape(t, m)
	le := make(map[string]bool)
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "gofy_worker_run_duration_seconds_bucket{") {
			continue
		}
		start := strings.Index(line, `le="`)
		if start < 0 {
			continue
		}
		rest := line[start+len(`le="`):]
		end := strings.Index(rest, `"`)
		if end > 0 {
			le[rest[:end]] = true
		}
	}
	for _, want := range []string{"0.005", "0.025", "10", "+Inf"} {
		if !le[want] {
			t.Fatalf("bucket %q is missing from the worker histogram: %v\n%s", want, le, body)
		}
	}
	for _, unwanted := range []string{"0", "50", "10000"} {
		if le[unwanted] {
			t.Fatalf("SDK default bucket %q leaked into the worker histogram\n%s", unwanted, body)
		}
	}
}

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	handler := m.Handler()
	if handler == nil {
		t.Fatal("Handler is nil")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, DefaultMetricsPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}
