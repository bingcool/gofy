package diagnostics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixtureScrape mirrors the shapes a real /metrics body has: counters, gauges,
// a histogram with escaped label values and exporter scope noise.
// fixtureScrape 模拟真实 /metrics 正文的形态：counter、gauge、带转义标签值的
// histogram，以及 exporter 的 scope 噪音。
const fixtureScrape = `# HELP gofy_worker_ready Whether every instance of a worker is ready.
# TYPE gofy_worker_ready gauge
gofy_worker_ready{group="group1",name="order-scan",otel_scope_name="github.com/bingcool/gofy/worker",otel_scope_schema_url="",otel_scope_version=""} 1
# HELP gofy_claim_claimed_total Rows claimed per poll round.
# TYPE gofy_claim_claimed_total counter
gofy_claim_claimed_total{job="order.settle",otel_scope_name="s",otel_scope_schema_url="",otel_scope_version=""} 84
# HELP gofy_claim_handler_duration_seconds Duration of the business handler, excluding bookkeeping writes.
# TYPE gofy_claim_handler_duration_seconds histogram
gofy_claim_handler_duration_seconds_bucket{job="order.settle",otel_scope_name="s",otel_scope_schema_url="",otel_scope_version="",outcome="succeeded",le="0.005"} 70
gofy_claim_handler_duration_seconds_bucket{job="order.settle",otel_scope_name="s",otel_scope_schema_url="",otel_scope_version="",outcome="succeeded",le="0.01"} 72
gofy_claim_handler_duration_seconds_bucket{job="order.settle",otel_scope_name="s",otel_scope_schema_url="",otel_scope_version="",outcome="succeeded",le="+Inf"} 72
gofy_claim_handler_duration_seconds_sum{job="order.settle",otel_scope_name="s",otel_scope_schema_url="",otel_scope_version="",outcome="succeeded"} 0.099395246
gofy_claim_handler_duration_seconds_count{job="order.settle",otel_scope_name="s",otel_scope_schema_url="",otel_scope_version="",outcome="succeeded"} 72
gofy_claim_handler_duration_seconds_bucket{job="order.settle",otel_scope_name="s",otel_scope_schema_url="",otel_scope_version="",outcome="dead",le="0.005"} 0
gofy_claim_handler_duration_seconds_bucket{job="order.settle",otel_scope_name="s",otel_scope_schema_url="",otel_scope_version="",outcome="dead",le="+Inf"} 3
gofy_claim_handler_duration_seconds_sum{job="order.settle",otel_scope_name="s",otel_scope_schema_url="",otel_scope_version="",outcome="dead"} 3.329e-05
gofy_claim_handler_duration_seconds_count{job="order.settle",otel_scope_name="s",otel_scope_schema_url="",otel_scope_version="",outcome="dead"} 3
`

func TestRenderFamiliesFormatsHumans(t *testing.T) {
	out := renderFamilies(parseExposition(fixtureScrape))

	// One section per family, header carries type and help.
	// 每个 family 一段，头部带类型与帮助文本。
	for _, want := range []string{
		"■ gofy_worker_ready  [gauge] — Whether every instance of a worker is ready.",
		"■ gofy_claim_claimed_total  [counter] — Rows claimed per poll round.",
		"■ gofy_claim_handler_duration_seconds  [histogram] — Duration of the business handler, excluding bookkeeping writes.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output is missing %q:\n%s", want, out)
		}
	}

	// Exporter noise labels are gone from every rendered row.
	// 渲染后的每一行都不再有 exporter 噪音标签。
	if strings.Contains(out, "otel_scope") {
		t.Fatalf("scope labels leaked into the view:\n%s", out)
	}

	// Counter and gauge values survive unchanged.
	// counter 与 gauge 的值原样保留。
	if !strings.Contains(out, "gofy_claim_claimed_total") || !strings.Contains(out, "job=order.settle") {
		t.Fatalf("counter row is malformed:\n%s", out)
	}

	// Histogram: merged rows with count/sum and interpolated quantiles. The
	// succeeded series' p50 rank (36) sits inside the first bucket (cum 70 of
	// 72), interpolating to ~2.6ms within (0, 0.005].
	// 直方图：合并行带 count/sum 与插值分位数。succeeded 的 p50 秩（36）落在
	// 第一个桶内（累计 70/72），在 (0, 0.005] 内插值约 2.6ms。
	if !strings.Contains(out, "count=72 sum=99.4ms") {
		t.Fatalf("histogram count/sum is wrong:\n%s", out)
	}
	if !strings.Contains(out, "p50≈2.6ms") {
		t.Fatalf("p50 was not interpolated into the first bucket:\n%s", out)
	}

	// The dead series has every sample past its last finite bucket (0.005 →
	// cumulative 0), so a quantile rank can only be bounded from below.
	// dead 序列的所有样本都越过最后一个有限桶（0.005 的累计为 0），分位数秩
	// 只能给出下界。
	if !strings.Contains(out, "outcome=dead") || !strings.Contains(out, "count=3 sum=33µs") {
		t.Fatalf("dead series row is malformed:\n%s", out)
	}
	if !strings.Contains(out, "p50≈>5.0ms") {
		t.Fatalf("inf-bucket quantile should print its lower bound:\n%s", out)
	}
}

func TestPrettyMetricsEndpoint(t *testing.T) {
	srv, err := New(Config{Enabled: true, DisablePprof: true, DisableHealth: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler, err := srv.Handler(Endpoints{
		Metrics: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(fixtureScrape))
		}),
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// The raw route keeps serving exposition untouched.
	// 原始路由继续原样提供 exposition。
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "gofy_worker_ready{group=") {
		t.Fatalf("/metrics changed: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// The formatted route renders the same data for humans.
	// 格式化路由把同一份数据渲染成人能读的形式。
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics-format", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics-format status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("/metrics-format content type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "■ gofy_worker_ready") || strings.Contains(body, "otel_scope") {
		t.Fatalf("/metrics-format body is not the rendered view:\n%s", body)
	}
}

func TestPrettyMetricsPassesErrorsThrough(t *testing.T) {
	srv, err := New(Config{Enabled: true, DisablePprof: true, DisableHealth: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler, err := srv.Handler(Endpoints{
		Metrics: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("collector unavailable"))
		}),
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics-format", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "collector unavailable") {
		t.Fatalf("error passthrough broken: status=%d body=%q", rec.Code, rec.Body.String())
	}
}
