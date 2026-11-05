package gofy_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy/gofytest"
	"github.com/bingcool/gofy/transport"
	"go.uber.org/zap"
)

type testModule struct {
	name  string
	order *recorder
	start error
	reg   func(*transport.Router) error
}

func (m *testModule) Name() string { return m.name }

func (m *testModule) Register(r *transport.Router) error {
	if m.reg != nil {
		return m.reg(r)
	}
	return nil
}

func (m *testModule) Start(context.Context) error {
	m.order.add("start:" + m.name)
	return m.start
}

func (m *testModule) Stop(context.Context) error {
	m.order.add("stop:" + m.name)
	return nil
}

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(event string) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

func withTestHTTP(t *testing.T, port int) gofy.Option {
	return gofy.WithHTTP(gofy.HTTPConfig{
		Host: "127.0.0.1",
		Port: port,
		Mode: "test",
	})
}

func waitForServer(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/healthz"
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server on port %d did not become ready", port)
}

type orderRequest struct {
	ID uint64 `uri:"id" validate:"required,min=1" message:"min:订单ID非法"`
}

type orderResponse struct {
	ID uint64 `json:"id"`
}

func TestNewDoesNotTouchTheFilesystem(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "nested", "app.pid")
	app, err := gofy.New(gofy.WithPIDFile(pidPath))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(pidPath)); !os.IsNotExist(err) {
		t.Fatalf("New created the pid directory: %v", err)
	}
	if app.Name() != "app" {
		t.Fatalf("name = %q, want app", app.Name())
	}
	if app.Logger() == nil {
		t.Fatal("Logger is nil")
	}
	if app.Version().GoVersion == "" {
		t.Fatal("Version has no Go version")
	}
}

func TestHandlerServesTypedRoutes(t *testing.T) {
	module := &testModule{
		name: "order",
		reg: func(r *transport.Router) error {
			transport.GET(r, "/orders/:id", func(_ context.Context, req *orderRequest) (*orderResponse, error) {
				return &orderResponse{ID: req.ID}, nil
			})
			transport.POST(r, "/orders", func(_ context.Context, _ *struct{}) (*orderResponse, error) {
				return nil, apperr.NotFound("order not found")
			})
			transport.GET(r, "/panic", func(context.Context, *struct{}) (*orderResponse, error) {
				panic("boom: secret")
			})
			return nil
		},
	}
	app := gofytest.New(t,
		gofy.WithName("order-service-test"),
		gofy.WithHealthEndpoints(true),
		gofy.WithModules(module),
	)

	rr := app.GET("/api/v1/order/orders/7", "X-Request-ID", "req-7")
	gofytest.Status(t, rr, http.StatusOK)
	body := gofytest.RawJSON(t, rr)
	if body["req_id"] != "req-7" {
		t.Fatalf("req_id = %v, want req-7", body["req_id"])
	}
	data, _ := body["data"].(map[string]any)
	if data["id"] != float64(7) {
		t.Fatalf("data = %v", body["data"])
	}

	rr = app.GET("/api/v1/order/orders/0")
	gofytest.Status(t, rr, http.StatusBadRequest)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(40001) {
		t.Fatalf("validation code = %v", code)
	}

	rr = app.POSTJSON("/api/v1/order/orders", map[string]any{})
	gofytest.Status(t, rr, http.StatusNotFound)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(40400) {
		t.Fatalf("apperr code = %v", code)
	}

	rr = app.GET("/api/v1/order/panic")
	gofytest.Status(t, rr, http.StatusInternalServerError)
	if body := rr.Body.String(); contains(body, "secret") || contains(body, "panic") {
		t.Fatalf("panic response leaked internals: %s", body)
	}

	rr = app.GET("/healthz")
	gofytest.Status(t, rr, http.StatusOK)
}

func TestModulesStartInOrderAndStopInReverse(t *testing.T) {
	rec := &recorder{}
	port := freePort(t)

	app, err := gofy.New(
		gofy.WithName("lifecycle-test"),
		withTestHTTP(t, port),
		gofy.WithHealthEndpoints(true),
		gofy.WithModules(
			&testModule{name: "one", order: rec},
			&testModule{name: "two", order: rec},
			&testModule{name: "three", order: rec},
		),
		gofy.WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(context.Background()) }()
	waitForServer(t, port)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}

	want := []string{"start:one", "start:two", "start:three", "stop:three", "stop:two", "stop:one"}
	got := rec.snapshot()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
}

func TestStartFailureStopsAlreadyStartedModules(t *testing.T) {
	rec := &recorder{}
	port := freePort(t)

	app, err := gofy.New(
		gofy.WithName("start-failure-test"),
		withTestHTTP(t, port),
		gofy.WithModules(
			&testModule{name: "one", order: rec},
			&testModule{name: "two", order: rec, start: errors.New("boom")},
			&testModule{name: "three", order: rec},
		),
		gofy.WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = app.Run(context.Background())
	if err == nil || !contains(err.Error(), "boom") {
		t.Fatalf("Run error = %v, want start failure", err)
	}
	want := []string{"start:one", "start:two", "stop:one"}
	if got := rec.snapshot(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
}

func TestPortInUseFailsSynchronously(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	app, err := gofy.New(gofy.WithName("port-test"), withTestHTTP(t, port), gofy.WithLogger(zap.NewNop()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded on an occupied port")
	}
}

func TestSingleShotReturnsErrAlreadyStarted(t *testing.T) {
	app, err := gofy.New(gofy.WithName("single-shot"), gofy.WithLogger(zap.NewNop()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := app.Handler(context.Background()); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if err := app.Run(context.Background()); !errors.Is(err, gofy.ErrAlreadyStarted) {
		t.Fatalf("Run error = %v, want ErrAlreadyStarted", err)
	}
	if _, err := app.Handler(context.Background()); !errors.Is(err, gofy.ErrAlreadyStarted) {
		t.Fatalf("Handler error = %v, want ErrAlreadyStarted", err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	app, err := gofy.New(gofy.WithName("idempotent"), gofy.WithLogger(zap.NewNop()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := app.Handler(context.Background()); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown #%d: %v", i+1, err)
		}
	}
}

func TestShutdownBeforeStartIsNoop(t *testing.T) {
	app, err := gofy.New(gofy.WithName("never-started"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestPIDFileLifecycle(t *testing.T) {
	port := freePort(t)
	pidPath := filepath.Join(t.TempDir(), "run", "app.pid")

	app, err := gofy.New(
		gofy.WithName("pid-test"),
		withTestHTTP(t, port),
		gofy.WithHealthEndpoints(true),
		gofy.WithPIDFile(pidPath),
		gofy.WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(context.Background()) }()
	waitForServer(t, port)

	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	if got, want := string(data), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("pid file = %q, want %q", got, want)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("pid file still exists after shutdown: %v", err)
	}
}

func TestStalePIDFileIsOverwritten(t *testing.T) {
	port := freePort(t)
	pidPath := filepath.Join(t.TempDir(), "app.pid")
	if err := os.WriteFile(pidPath, []byte("999999"), 0o644); err != nil {
		t.Fatalf("write stale pid: %v", err)
	}

	app, err := gofy.New(
		gofy.WithName("stale-pid-test"),
		withTestHTTP(t, port),
		gofy.WithHealthEndpoints(true),
		gofy.WithPIDFile(pidPath),
		gofy.WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(context.Background()) }()
	waitForServer(t, port)

	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	if got, want := string(data), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("pid file = %q, want %q", got, want)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	<-errCh
}

func TestExecuteUnknownCommandUsesUsageExitCode(t *testing.T) {
	app, err := gofy.New(gofy.WithName("cli-test"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = app.ExecuteArgs(context.Background(), []string{"nope"})
	if !errors.Is(err, gofy.ErrUnknownCommand) || !errors.Is(err, gofy.ErrUsage) {
		t.Fatalf("ExecuteArgs error = %v", err)
	}
	if code := gofy.ExitCode(err); code != gofy.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, gofy.ExitUsage)
	}
}

func TestExecuteStopWithoutPIDFile(t *testing.T) {
	app, err := gofy.New(gofy.WithName("cli-test"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = app.ExecuteArgs(context.Background(), []string{"stop", "--force=1"})
	if !errors.Is(err, gofy.ErrPIDFileDisabled) {
		t.Fatalf("ExecuteArgs error = %v, want ErrPIDFileDisabled", err)
	}
}

func TestExecuteStopNotRunning(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "app.pid")
	app, err := gofy.New(gofy.WithName("cli-test"), gofy.WithPIDFile(pidPath))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = app.ExecuteArgs(context.Background(), []string{"stop", "--force=1"})
	if !errors.Is(err, gofy.ErrNotRunning) {
		t.Fatalf("ExecuteArgs error = %v, want ErrNotRunning", err)
	}
}

func TestExecuteRejectsUnexpectedStartArguments(t *testing.T) {
	app, err := gofy.New(gofy.WithName("cli-test"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.ExecuteArgs(context.Background(), []string{"start", "extra"}); !errors.Is(err, gofy.ErrUsage) {
		t.Fatalf("ExecuteArgs error = %v, want ErrUsage", err)
	}
	if err := app.ExecuteArgs(context.Background(), []string{"version", "extra"}); !errors.Is(err, gofy.ErrUsage) {
		t.Fatalf("ExecuteArgs error = %v, want ErrUsage", err)
	}
}

func TestExitCode(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, gofy.ExitOK},
		{context.Canceled, gofy.ExitOK},
		{gofy.ErrUsage, gofy.ExitUsage},
		{fmt.Errorf("wrapped: %w", gofy.ErrUnknownCommand), gofy.ExitUsage},
		{errors.New("boom"), gofy.ExitError},
	}
	for _, tc := range cases {
		if got := gofy.ExitCode(tc.err); got != tc.want {
			t.Fatalf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
