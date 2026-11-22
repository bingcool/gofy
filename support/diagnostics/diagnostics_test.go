package diagnostics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"defaults apply", Config{Enabled: true}, false},
		{"explicit loopback", Config{Enabled: true, Addr: "127.0.0.1:9090"}, false},
		{"all interfaces", Config{Enabled: true, Addr: "0.0.0.0:6060"}, false},
		{"metrics only", Config{Enabled: true, DisablePprof: true, DisableHealth: true}, false},
		{"pprof only", Config{Enabled: true, DisableMetrics: true, DisableHealth: true}, false},
		{"health only", Config{Enabled: true, DisableMetrics: true, DisablePprof: true}, false},
		{"nothing enabled", Config{Enabled: true, DisableMetrics: true, DisablePprof: true, DisableHealth: true}, true},
		{"missing port", Config{Enabled: true, Addr: "127.0.0.1"}, true},
		{"bad metrics path", Config{Enabled: true, MetricsPath: "metrics"}, true},
		{"bad health path", Config{Enabled: true, HealthPath: "health"}, true},
		{"disabled without endpoints", Config{DisableMetrics: true, DisablePprof: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate accepted %+v", tc.cfg)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
	if err := (Config{Enabled: true, DisableMetrics: true, DisablePprof: true, DisableHealth: true}).Validate(); !errors.Is(err, ErrNothingEnabled) {
		t.Fatalf("all-disabled error = %v, want ErrNothingEnabled", err)
	}
}

func TestHandlerMountsEndpoints(t *testing.T) {
	srv, err := New(Config{Enabled: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler, err := srv.Handler(Endpoints{
		Metrics: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "scraped")
		}),
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	for _, tc := range []struct{ path, want string }{
		{"/metrics", "scraped"},
		{"/debug/pprof/", "goroutine"},
		{"/debug/pprof/goroutine?debug=1", "goroutine"},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", tc.path, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, tc.want) {
			t.Fatalf("%s: body does not contain %q", tc.path, tc.want)
		}
	}
}

func TestHandlerRespectsDisableFlags(t *testing.T) {
	srv, err := New(Config{Enabled: true, DisablePprof: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler, err := srv.Handler(Endpoints{Metrics: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("pprof status = %d, want 404 when disabled", rec.Code)
	}
}

func TestHandlerRejectsNilMetricsHandler(t *testing.T) {
	srv, err := New(Config{Enabled: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := srv.Handler(Endpoints{}); err == nil {
		t.Fatal("Handler accepted a nil metrics handler")
	}
}

func TestHandlerCustomMetricsPath(t *testing.T) {
	srv, err := New(Config{Enabled: true, MetricsPath: "/internal/metrics", DisablePprof: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler, err := srv.Handler(Endpoints{Metrics: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "custom")
	})})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "custom" {
		t.Fatalf("custom path: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestStartServesAndShutsDown(t *testing.T) {
	srv, err := New(Config{Enabled: true, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(Endpoints{Metrics: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "live")
	})}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	addr := srv.BoundAddr()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "live" {
		t.Fatalf("metrics: status=%d body=%q", resp.StatusCode, body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if _, err := client.Get("http://" + addr + "/metrics"); err == nil {
		t.Fatal("endpoint still reachable after Shutdown")
	}
}

func TestStartRejectsPortConflict(t *testing.T) {
	first, err := New(Config{Enabled: true, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New first: %v", err)
	}
	metrics := func(w http.ResponseWriter, _ *http.Request) {}
	if err := first.Start(Endpoints{Metrics: http.HandlerFunc(metrics)}); err != nil {
		t.Fatalf("Start first: %v", err)
	}
	defer func() { _ = first.Shutdown(context.Background()) }()

	second, err := New(Config{Enabled: true, Addr: first.BoundAddr()})
	if err != nil {
		t.Fatalf("New second: %v", err)
	}
	if err := second.Start(Endpoints{Metrics: http.HandlerFunc(metrics)}); err == nil {
		_ = second.Shutdown(context.Background())
		t.Fatal("Start succeeded on an occupied port")
	}
}

func TestDisabledServerDoesNotListen(t *testing.T) {
	srv, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv.Enabled() {
		t.Fatal("disabled config reported enabled")
	}
	if err := srv.Start(Endpoints{Metrics: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}); err != nil {
		t.Fatalf("Start on disabled server: %v", err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown on disabled server: %v", err)
	}
}

func TestShutdownWithoutStartIsNoop(t *testing.T) {
	srv, err := New(Config{Enabled: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	var nilServer *Server
	if nilServer.Enabled() {
		t.Fatal("nil Server reported enabled")
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:6060":  true,
		"localhost:6060":  true,
		"[::1]:6060":      true,
		"0.0.0.0:6060":    false,
		":6060":           false,
		"10.0.0.5:6060":   false,
		"example.com:606": false,
		"garbage":         false,
	}
	for addr, want := range cases {
		if got := isLoopbackAddr(addr); got != want {
			t.Fatalf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

// TestHandlerMountsHealthDetail pins the endpoint that makes one diagnostics
// port enough: /readyz decides traffic on the application listener, and /health
// explains which check failed without putting that detail on the public port.
// TestHandlerMountsHealthDetail 固定"一个诊断端口就够用"的那个端点：/readyz 在应用监听上
// 决定流量，/health 说明是哪个检查失败，同时不把明细放到业务端口上。
func TestHandlerMountsHealthDetail(t *testing.T) {
	detail := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ready":false}`)
	})
	// Health-only configuration: this is the deployment the relaxed
	// ErrNothingEnabled rule exists for.
	// 只开 health 的配置：放宽 ErrNothingEnabled 正是为了这种部署。
	srv, err := New(Config{Enabled: true, DisableMetrics: true, DisablePprof: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler, err := srv.Handler(Endpoints{Health: detail})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/health status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != `{"ready":false}` {
		t.Fatalf("/health body = %q", body)
	}
}

// TestHandlerHealthPathAndDisable pins the two escape hatches: a deployment can
// move the detail off the conventional path, or turn it off entirely, without
// losing metrics or pprof.
// TestHandlerHealthPathAndDisable 固定两个逃生口：部署可以把明细移到非约定路径，或完全
// 关掉它，而不影响 metrics 与 pprof。
func TestHandlerHealthPathAndDisable(t *testing.T) {
	detail := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "detail")
	})

	srv, err := New(Config{Enabled: true, DisablePprof: true, HealthPath: "/internal/health"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	moved, err := srv.Handler(Endpoints{
		Metrics: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		Health:  detail,
		// The request-level path wins over the config value.
		// 请求级路径优先于配置值。
		HealthPath: "/probe/health",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	moved.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe/health", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "detail" {
		t.Fatalf("moved path: status=%d body=%q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	moved.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("default path status = %d, want 404 after the move", rec.Code)
	}

	// Disabled: the handler is not mounted, and the listener still serves
	// metrics.
	// 关闭：不挂载 handler，监听仍提供 metrics。
	off, err := New(Config{Enabled: true, DisablePprof: true, DisableHealth: true})
	if err != nil {
		t.Fatalf("New(disabled health): %v", err)
	}
	withoutHealth, err := off.Handler(Endpoints{
		Metrics: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "scraped")
		}),
		Health: detail,
	})
	if err != nil {
		t.Fatalf("Handler(disabled health): %v", err)
	}
	rec = httptest.NewRecorder()
	withoutHealth.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled /health status = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	withoutHealth.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "scraped" {
		t.Fatalf("metrics with health disabled: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

// TestClampPprofSeconds pins the bound on profile and trace. Both standard
// handlers stream for as long as `seconds` asks, so an unbounded value lets one
// request hold a goroutine and the profiler; the wrapper must rewrite only that
// parameter, and only when it is too large.
// TestClampPprofSeconds 固定 profile 与 trace 的上限。两个标准处理器都按 `seconds`
// 要求持续输出，无界值会让一个请求占住 goroutine 与剖析器；包装器只能改写这个参数，
// 且只在它过大时改写。
func TestClampPprofSeconds(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"oversized", "seconds=9999", "60"},
		{"at the bound", "seconds=60", "60"},
		{"below the bound", "seconds=5", "5"},
		{"absent", "", ""},
		{"not a number", "seconds=abc", "abc"},
		{"other parameters survive", "seconds=9999&debug=1", "60"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			var debug string
			h := clampPprofSeconds(func(_ http.ResponseWriter, r *http.Request) {
				got = r.URL.Query().Get("seconds")
				debug = r.URL.Query().Get("debug")
			})
			req := httptest.NewRequest(http.MethodGet, "/debug/pprof/profile?"+tc.query, nil)
			h(httptest.NewRecorder(), req)

			if got != tc.want {
				t.Fatalf("seconds seen by the handler = %q, want %q", got, tc.want)
			}
			// The clone must not leak back into the caller's request.
			// 克隆出来的请求不能反向污染调用方的请求。
			original := req.URL.Query().Get("seconds")
			if tc.query == "seconds=9999" && original != "9999" {
				t.Fatalf("original request mutated: seconds = %q", original)
			}
			if strings.Contains(tc.query, "debug=1") && debug != "1" {
				t.Fatalf("debug = %q, want 1", debug)
			}
		})
	}
}
