package gofy_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	"github.com/bingcool/gofy/logger"
	gofyscript "github.com/bingcool/gofy/script"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/middleware"
	"github.com/gin-gonic/gin"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
)

type testModule struct {
	name  string
	order *recorder
	start error
	reg   func(*support.Router) error
}

func (m *testModule) Name() string { return m.name }

func (m *testModule) Register(r *support.Router) error {
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

type prefixModule struct {
	testModule
	prefix string
}

func (m *prefixModule) Prefix() string { return m.prefix }

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
		reg: func(r *support.Router) error {
			support.GET(r, "/orders/:id", func(_ context.Context, req *orderRequest) (*orderResponse, error) {
				return &orderResponse{ID: req.ID}, nil
			})
			support.POST(r, "/orders", func(_ context.Context, _ *struct{}) (*orderResponse, error) {
				return nil, apperr.NotFound("order not found")
			})
			support.GET(r, "/panic", func(context.Context, *struct{}) (*orderResponse, error) {
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

	rr := app.GET("/order/orders/7", "X-Trace-ID", "req-7")
	gofytest.Status(t, rr, http.StatusOK)
	body := gofytest.RawJSON(t, rr)
	if body["req_id"] != "req-7" {
		t.Fatalf("req_id = %v, want req-7", body["req_id"])
	}
	data, _ := body["data"].(map[string]any)
	if data["id"] != float64(7) {
		t.Fatalf("data = %v", body["data"])
	}

	rr = app.GET("/order/orders/0")
	gofytest.Status(t, rr, http.StatusBadRequest)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(40001) {
		t.Fatalf("validation code = %v", code)
	}

	rr = app.POSTJSON("/order/orders", map[string]any{})
	gofytest.Status(t, rr, http.StatusNotFound)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(40400) {
		t.Fatalf("apperr code = %v", code)
	}

	rr = app.GET("/order/panic")
	gofytest.Status(t, rr, http.StatusInternalServerError)
	if body := rr.Body.String(); contains(body, "secret") || contains(body, "panic") {
		t.Fatalf("panic response leaked internals: %s", body)
	}

	rr = app.GET("/healthz")
	gofytest.Status(t, rr, http.StatusOK)
}

// TestModulesCanShareTheRootPrefix 固定「多个模块都返回 Prefix() == ""」的用法：
// 契约分组自带完整部署前缀，模块根只负责把各自分组的完整路径挂到根 Router，
// 框架不能按挂载点把它们判成冲突。
func TestModulesCanShareTheRootPrefix(t *testing.T) {
	orderModule := &prefixModule{
		testModule: testModule{name: "order", reg: func(r *support.Router) error {
			support.GET(r, "/api/v1/order/orders/:id", func(_ context.Context, req *orderRequest) (*orderResponse, error) {
				return &orderResponse{ID: req.ID}, nil
			})
			return nil
		}},
	}
	productModule := &prefixModule{
		testModule: testModule{name: "product", reg: func(r *support.Router) error {
			support.GET(r, "/api/v1/product/products/:id", func(_ context.Context, req *orderRequest) (*orderResponse, error) {
				return &orderResponse{ID: req.ID}, nil
			})
			return nil
		}},
	}
	app := gofytest.New(t,
		gofy.WithName("root-prefix-test"),
		gofy.WithModules(orderModule, productModule),
	)

	for _, path := range []string{"/api/v1/order/orders/7", "/api/v1/product/products/9"} {
		rr := app.GET(path)
		gofytest.Status(t, rr, http.StatusOK)
		if data, _ := gofytest.RawJSON(t, rr)["data"].(map[string]any); data["id"] == nil {
			t.Fatalf("%s: data = %v", path, data)
		}
	}
}

// TestDuplicateNonRootPrefixIsRejected 确认非空挂载点仍按前缀去重。
func TestDuplicateNonRootPrefixIsRejected(t *testing.T) {
	app, err := gofy.New(
		gofy.WithLogger(zap.NewNop()),
		gofy.WithModules(
			&prefixModule{testModule: testModule{name: "one"}, prefix: "/same"},
			&prefixModule{testModule: testModule{name: "two"}, prefix: "/same"},
		),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := app.Handler(context.Background()); err == nil || !strings.Contains(err.Error(), "used by both") {
		t.Fatalf("Handler error = %v, want duplicate prefix error", err)
	}
}

func TestRecoveryWritesSystemErrorLogOnly(t *testing.T) {
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "info.log")
	errorPath := filepath.Join(dir, "error.log")
	systemPath := filepath.Join(dir, "system_error.log")

	module := &testModule{
		name: "panic",
		reg: func(r *support.Router) error {
			support.GET(r, "/boom", func(context.Context, *struct{}) (*orderResponse, error) {
				panic("boom: secret")
			})
			return nil
		},
	}
	app, err := gofy.New(
		gofy.WithName("log-test"),
		gofy.WithPIDFile(""),
		gofy.WithAccessLog(false),
		gofy.WithRecovery(true),
		gofy.WithLogConfig(logger.Config{
			Outputs:            []string{infoPath},
			ErrorOutputs:       []string{errorPath},
			SystemErrorOutputs: []string{systemPath},
		}),
		gofy.WithModules(module),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler, err := app.Handler(context.Background())
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/panic/boom", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rr.Code)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	systemLog := string(mustReadFile(t, systemPath))
	if !strings.Contains(systemLog, "panic recovered") || !strings.Contains(systemLog, "boom: secret") {
		t.Fatalf("system log = %q", systemLog)
	}
	if strings.Contains(string(mustReadFile(t, infoPath)), "panic recovered") {
		t.Fatal("panic was written to info.log")
	}
	if strings.Contains(string(mustReadFile(t, errorPath)), "panic recovered") {
		t.Fatal("panic was written to error.log")
	}
}

func TestWithAccessLogOptionsAppliesCustomOptions(t *testing.T) {
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "info.log")

	module := &testModule{
		name: "catalog",
		reg: func(r *support.Router) error {
			support.GET(r, "/ping", func(context.Context, *struct{}) (*orderResponse, error) {
				return &orderResponse{ID: 1}, nil
			})
			return nil
		},
	}
	app, err := gofy.New(
		gofy.WithName("accesslog-options-test"),
		gofy.WithPIDFile(""),
		gofy.WithHealthEndpoints(true),
		// 自定义配置按字面生效：只跳过模块路由，默认的健康检查跳过不再生效。
		gofy.WithAccessLogOptions(middleware.AccessLogOptions{
			SkipPaths: []string{"/catalog/ping"},
		}),
		gofy.WithLogConfig(logger.Config{Outputs: []string{infoPath}}),
		gofy.WithModules(module),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler, err := app.Handler(context.Background())
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/catalog/ping", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("ping status = %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("health status = %d", rr.Code)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	logs := string(mustReadFile(t, infoPath))
	if strings.Contains(logs, "/catalog/ping") {
		t.Fatalf("skip path was logged: %s", logs)
	}
	if !strings.Contains(logs, "/healthz") {
		t.Fatalf("health check was not logged under custom options: %s", logs)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
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

func TestExecuteScriptFlagWithoutDashesIsUsageError(t *testing.T) {
	app, err := gofy.New(
		gofy.WithName("cli-test"),
		gofy.WithScripts(gofyscript.Script{
			Name: "demo",
			Flags: func(fs *pflag.FlagSet) {
				fs.Uint64("order-id", 0, "order id")
			},
			Run: func(context.Context, *gofyscript.Runtime, *pflag.FlagSet) error { return nil },
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	oldArgs, oldStderr := os.Args, os.Stderr
	t.Cleanup(func() {
		os.Args = oldArgs
		os.Stderr = oldStderr
	})
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Args = []string{"cli-test", "script", "demo", "order-id=1222"}
	os.Stderr = w
	execErr := app.Execute(context.Background())
	os.Stderr = oldStderr
	_ = w.Close()

	if !errors.Is(execErr, gofy.ErrUsage) {
		t.Fatalf("Execute error = %v, want ErrUsage", execErr)
	}
	if code := gofy.ExitCode(execErr); code != gofy.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, gofy.ExitUsage)
	}
	output, _ := io.ReadAll(r)
	if got := string(output); !strings.Contains(got, "leading --") || !strings.Contains(got, "--order-id=1222") {
		t.Fatalf("stderr = %q, want a leading -- hint", got)
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

func reloadFactory(body string, events *recorder) gofy.ModuleFactory {
	return func(*gofy.Runtime) (support.Module, error) {
		return &testModule{
			name:  "reload",
			order: events,
			reg: func(r *support.Router) error {
				r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, body) })
				return nil
			},
		}, nil
	}
}

func TestReloadSwapsRoutesInPlace(t *testing.T) {
	port := freePort(t)
	events := &recorder{}
	app, err := gofy.New(
		gofy.WithName("reload-test"),
		withTestHTTP(t, port),
		gofy.WithHealthEndpoints(true),
		gofy.WithModuleFactories(reloadFactory("v1", events)),
		gofy.WithShutdownTimeout(2*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(context.Background()) }()
	waitForServer(t, port)

	if got := getText(t, port, "/reload/ping"); got != "v1" {
		t.Fatalf("before reload: %q", got)
	}

	err = app.Reload(context.Background(), gofy.ReloadSpec{
		Env:             "prd",
		ModuleFactories: []gofy.ModuleFactory{reloadFactory("v2", events)},
	})
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := getText(t, port, "/reload/ping"); got != "v2" {
		t.Fatalf("after reload: %q", got)
	}
	if got := countEvents(events, "stop:reload"); got != 1 {
		t.Fatalf("old module stop count = %d, want 1", got)
	}

	// An invalid spec must leave the running routes untouched.
	err = app.Reload(context.Background(), gofy.ReloadSpec{
		Database:        &gofy.DatabaseConfig{},
		ModuleFactories: []gofy.ModuleFactory{reloadFactory("v3", events)},
	})
	if err == nil {
		t.Fatal("Reload accepted an empty DSN")
	}
	if got := getText(t, port, "/reload/ping"); got != "v2" {
		t.Fatalf("after rejected reload: %q", got)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}
}

// TestReloadRebuildsMiddlewareRegistry 证明注册表由 App 按次装配创建：
// 模块只按名字解析，Reload 传入新的 registrar 后新路由立即使用新实例。
func TestReloadRebuildsMiddlewareRegistry(t *testing.T) {
	port := freePort(t)
	events := &recorder{}
	registrar := func(version string) gofy.MiddlewareRegistrar {
		return func(reg gofy.MiddlewareRegistration) error {
			return reg.Registry.Register("stamp", func(c *gin.Context) {
				c.String(http.StatusOK, version)
			})
		}
	}
	factory := func(*gofy.Runtime) (support.Module, error) {
		return &testModule{
			name:  "reload",
			order: events,
			reg: func(r *support.Router) error {
				r.GET("/stamp", func(c *gin.Context) {
					mw, ok := r.Middleware("stamp")
					if !ok {
						c.String(http.StatusInternalServerError, "missing")
						return
					}
					mw(c)
				})
				return nil
			},
		}, nil
	}
	app, err := gofy.New(
		gofy.WithName("reload-registry"),
		withTestHTTP(t, port),
		gofy.WithHealthEndpoints(true),
		gofy.WithMiddlewareRegistrar(registrar("v1")),
		gofy.WithModuleFactories(factory),
		gofy.WithShutdownTimeout(2*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(context.Background()) }()
	waitForServer(t, port)

	if got := getText(t, port, "/reload/stamp"); got != "v1" {
		t.Fatalf("before reload: %q", got)
	}

	if err := app.Reload(context.Background(), gofy.ReloadSpec{
		MiddlewareRegistrars: []gofy.MiddlewareRegistrar{registrar("v2")},
	}); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := getText(t, port, "/reload/stamp"); got != "v2" {
		t.Fatalf("after reload: %q", got)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}
}

func TestReloadRequiresRunningServer(t *testing.T) {
	app, err := gofy.New(gofy.WithName("reload-idle"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Reload(context.Background(), gofy.ReloadSpec{}); !errors.Is(err, gofy.ErrNotRunning) {
		t.Fatalf("Reload error = %v, want ErrNotRunning", err)
	}
}

func TestReloadKeepsInjectedModulesRunning(t *testing.T) {
	port := freePort(t)
	events := &recorder{}
	embedded := &testModule{
		name:  "embedded",
		order: events,
		reg: func(r *support.Router) error {
			r.GET("/key", func(c *gin.Context) { c.String(http.StatusOK, "static") })
			return nil
		},
	}
	app, err := gofy.New(
		gofy.WithName("reload-injected"),
		withTestHTTP(t, port),
		gofy.WithHealthEndpoints(true),
		gofy.WithModules(embedded),
		gofy.WithModuleFactories(reloadFactory("v1", events)),
		gofy.WithShutdownTimeout(2*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(context.Background()) }()
	waitForServer(t, port)

	if err := app.Reload(context.Background(), gofy.ReloadSpec{
		ModuleFactories: []gofy.ModuleFactory{reloadFactory("v2", events)},
	}); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := getText(t, port, "/embedded/key"); got != "static" {
		t.Fatalf("injected route after reload: %q", got)
	}
	if got := countEvents(events, "start:embedded"); got != 1 {
		t.Fatalf("injected start count = %d, want 1", got)
	}
	if got := countEvents(events, "stop:embedded"); got != 0 {
		t.Fatalf("injected stop count after reload = %d, want 0", got)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}
	if got := countEvents(events, "stop:embedded"); got != 1 {
		t.Fatalf("injected stop count after shutdown = %d, want 1", got)
	}
}

func getText(t *testing.T, port int, path string) string {
	t.Helper()
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func countEvents(r *recorder, want string) int {
	count := 0
	for _, event := range r.snapshot() {
		if event == want {
			count++
		}
	}
	return count
}
