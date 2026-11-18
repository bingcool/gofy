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
		{"metrics only", Config{Enabled: true, DisablePprof: true}, false},
		{"pprof only", Config{Enabled: true, DisableMetrics: true}, false},
		{"nothing enabled", Config{Enabled: true, DisableMetrics: true, DisablePprof: true}, true},
		{"missing port", Config{Enabled: true, Addr: "127.0.0.1"}, true},
		{"bad metrics path", Config{Enabled: true, MetricsPath: "metrics"}, true},
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
	if err := (Config{Enabled: true, DisableMetrics: true, DisablePprof: true}).Validate(); !errors.Is(err, ErrNothingEnabled) {
		t.Fatalf("both-disabled error = %v, want ErrNothingEnabled", err)
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
