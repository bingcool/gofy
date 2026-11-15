package gofy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/bingcool/gofy/internal/pidfile"
)

func TestDuplicatePID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.pid")
	self := os.Getpid()

	if _, ok := duplicatePID("", self); ok {
		t.Fatal("empty pid path reported a duplicate")
	}
	if _, ok := duplicatePID(filepath.Join(dir, "missing.pid"), self); ok {
		t.Fatal("missing pid file reported a duplicate")
	}

	if err := pidfile.Write(path, self, 0o600); err != nil {
		t.Fatalf("write own pid: %v", err)
	}
	if _, ok := duplicatePID(path, self); ok {
		t.Fatal("own pid reported a duplicate after a self re-exec")
	}

	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getppid())), 0o600); err != nil {
		t.Fatalf("write parent pid: %v", err)
	}
	if _, ok := duplicatePID(path, self); !ok {
		t.Fatal("a live pid belonging to another process was not reported")
	}
}

func TestStopAcceptingRefusesNewRequestsButDrainsInFlight(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Slow") == "1" {
			close(entered)
			<-release
		}
		w.WriteHeader(http.StatusOK)
	})
	swap := newHandlerSwap(newGeneration(1, handler), app.admission)

	before := httptest.NewRecorder()
	swap.ServeHTTP(before, httptest.NewRequest(http.MethodGet, "/", nil))
	if before.Code != http.StatusOK {
		t.Fatalf("status before StopAccepting = %d, want 200", before.Code)
	}

	slowReq := httptest.NewRequest(http.MethodGet, "/slow", nil)
	slowReq.Header.Set("X-Slow", "1")
	slowRec := httptest.NewRecorder()
	slowDone := make(chan int, 1)
	go func() {
		swap.ServeHTTP(slowRec, slowReq)
		slowDone <- slowRec.Code
	}()
	<-entered

	if err := app.StopAccepting(context.Background()); err != nil {
		t.Fatalf("StopAccepting: %v", err)
	}
	if err := app.StopAccepting(context.Background()); err != nil {
		t.Fatalf("StopAccepting twice: %v", err)
	}

	after := httptest.NewRecorder()
	swap.ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/", nil))
	if after.Code != http.StatusServiceUnavailable {
		t.Fatalf("status after StopAccepting = %d, want 503", after.Code)
	}
	if got := after.Header().Get("Connection"); got != "close" {
		t.Fatalf("Connection = %q, want close", got)
	}

	close(release)
	if code := <-slowDone; code != http.StatusOK {
		t.Fatalf("in-flight request status = %d, want 200 (drain must not cut it)", code)
	}
}

func TestStopAcceptingBeforeServerExists(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !app.admission.isOpen() {
		t.Fatal("a new app must accept requests")
	}
	if err := app.StopAccepting(context.Background()); err != nil {
		t.Fatalf("StopAccepting before Run: %v", err)
	}
	if app.admission.isOpen() {
		t.Fatal("gate stayed open before the server was built")
	}
}

func TestStopAcceptingOverRealListener(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	app, err := New(
		WithHTTP(HTTPConfig{Host: "127.0.0.1", Port: port, Mode: "test"}),
		WithHealthEndpoints(true),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- app.Run(ctx) }()

	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("server never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := app.StopAccepting(ctx); err != nil {
		t.Fatalf("StopAccepting: %v", err)
	}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("request after StopAccepting failed (listener should stay open): %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("real HTTP status after StopAccepting = %d, want 503", resp.StatusCode)
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run: %v", err)
	}
}
