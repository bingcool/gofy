package gofy_test

import (
	"context"
	"encoding/json"
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
	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/gofytest"
	"github.com/bingcool/gofy/internal/pidfile"
	"github.com/bingcool/gofy/logger"
	gofyscript "github.com/bingcool/gofy/script"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/bingcool/gofy/support/middleware"
	gofyworker "github.com/bingcool/gofy/support/worker"
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

// blockingStartModule blocks in Start until release is closed, so Reload stalls
// before publishing the next generation and a race with Shutdown can be created.
// blockingStartModule 在 Start 中等待 release，用来让 Reload 停在发布新代
// 之前，制造与 Shutdown 的竞争窗口。
type blockingStartModule struct {
	*testModule
	entered chan struct{}
	release chan struct{}
}

func (m *blockingStartModule) Start(ctx context.Context) error {
	m.order.add("start:" + m.name)
	close(m.entered)
	select {
	case <-m.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

// TestVersionFallsBackToTheFramework pins the default an application that never
// calls WithVersionInfo sees: the framework version, not "dev" or a module
// version that a local replace turns into v0.0.0.
// TestVersionFallsBackToTheFramework 固定"未调用 WithVersionInfo 的应用"看到的默认值：
// 框架版本，而不是 dev，也不是被本地 replace 变成 v0.0.0 的模块版本。
func TestVersionFallsBackToTheFramework(t *testing.T) {
	app, err := gofy.New(gofy.WithPIDFile(filepath.Join(t.TempDir(), "app.pid")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := app.Version().Version; got != gofy.Version {
		t.Fatalf("Version().Version = %q, want gofy.Version %q", got, gofy.Version)
	}
	if gofy.Version != "1.0.0" {
		t.Fatalf("gofy.Version = %q; changing it means a release", gofy.Version)
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

// TestModulesCanShareTheRootPrefix pins down the usage where several modules all
// return Prefix() == "": the contract group already carries the full deployment
// prefix and each module root only mounts its groups' full paths on the root Router,
// so the framework must not judge them as conflicting mount points.
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

// TestDuplicateNonRootPrefixIsRejected confirms non-empty mount points are still
// deduplicated by prefix.
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

// TestMiddlewareOrderContract pins down the middleware install order of
// buildHTTPWith: framework middleware runs before business global middleware,
// business global middleware before module group/route middleware, and the handler
// comes last. Any change that alters the relative order fails here.
// TestMiddlewareOrderContract 固定 buildHTTPWith 的中间件安装顺序：框架内建
// 中间件先于业务全局中间件执行，业务全局中间件先于模块分组/路由中间件，最后
// 才是 handler。任何调整若改变相对顺序都会在这里失败。
func TestMiddlewareOrderContract(t *testing.T) {
	var (
		mu       sync.Mutex
		observed []string
	)
	record := func(name string) {
		mu.Lock()
		observed = append(observed, name)
		mu.Unlock()
	}
	marker := func(name string, checks ...func(*gin.Context)) support.Middleware {
		return func(c *gin.Context) {
			for _, check := range checks {
				check(c)
			}
			record(name)
			c.Next()
		}
	}
	// Framework side effects visible to business middleware, used to prove the built-in
	// middleware already ran.
	// 业务中间件可见的框架副作用，用来确认内建中间件已经执行过。
	assertBuiltinsRan := func(c *gin.Context) {
		if c.GetString(support.RequestIDKey) == "" {
			t.Error("business middleware ran before RequestID")
		}
		if got := headerpropagation.HeadersFromContext(c.Request.Context()).Get(headerpropagation.HeaderTraceID); got == "" {
			t.Error("business middleware ran before HeaderPropagation")
		}
		if c.Writer.Header().Get("X-Content-Type-Options") == "" {
			t.Error("business middleware ran before SecurityHeaders")
		}
		if c.Writer.Header().Get("Access-Control-Allow-Origin") == "" {
			t.Error("business middleware ran before CORS")
		}
		if _, err := io.ReadAll(c.Request.Body); err == nil {
			t.Error("business middleware ran before BodyLimit")
		}
	}

	module := &testModule{
		name: "contract",
		reg: func(r *support.Router) error {
			r.Use(marker("group"))
			r.POST("/order", func(c *gin.Context) {
				record("handler")
				c.Status(http.StatusNoContent)
			}, marker("route"))
			return nil
		},
	}
	app, err := gofy.New(
		gofy.WithName("middleware-order"),
		gofy.WithPIDFile(""),
		gofy.WithAccessLog(false),
		gofy.WithRecovery(true),
		gofy.WithHeaderPropagation(true),
		gofy.WithSecurityHeaders(support.SecurityHeadersConfig{}),
		gofy.WithCORS(support.CORSConfig{AllowOrigins: []string{"example.com"}}),
		gofy.WithMaxBodyBytes(1024),
		gofy.WithMiddleware(marker("app-1", assertBuiltinsRan), marker("app-2")),
		gofy.WithModules(module),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler, err := app.Handler(context.Background())
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/contract/order", strings.NewReader(strings.Repeat("a", 2048)))
	req.Header.Set("Origin", "https://example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), observed...)
	mu.Unlock()
	want := []string{"app-1", "app-2", "group", "route", "handler"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("middleware order = %v, want %v", got, want)
	}
}

// TestPanicResponseKeepsEnvelopeAndAccessLog proves a panic stays inside the unified
// Envelope after Recovery, the outer AccessLog records the final 500 and req_id is
// not lost. trace_id only reaches the access log when telemetry is enabled, so it is
// not covered here.
// TestPanicResponseKeepsEnvelopeAndAccessLog 证明 panic 经过 Recovery 后仍是
// 统一 Envelope，外层 AccessLog 记录最终 500，且 req_id 没有丢。trace_id 只在
// 启用 telemetry 时写入访问日志，这里不覆盖。
func TestPanicResponseKeepsEnvelopeAndAccessLog(t *testing.T) {
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "info.log")

	module := &testModule{
		name: "panic",
		reg: func(r *support.Router) error {
			r.GET("/boom", func(*gin.Context) { panic("boom: hunter2") })
			return nil
		},
	}
	app, err := gofy.New(
		gofy.WithName("panic-contract"),
		gofy.WithPIDFile(""),
		gofy.WithAccessLog(true),
		gofy.WithRecovery(true),
		gofy.WithLogConfig(logger.Config{
			Outputs:            []string{infoPath},
			ErrorOutputs:       []string{infoPath},
			SystemErrorOutputs: []string{infoPath},
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

	req := httptest.NewRequest(http.MethodGet, "/panic/boom", nil)
	req.Header.Set(support.DefaultRequestIDHeader, "test-trace-001")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if body := rr.Body.String(); strings.Contains(body, "hunter2") || strings.Contains(body, "goroutine") {
		t.Fatalf("panic details leaked: %s", body)
	}
	var envelope struct {
		Code  int    `json:"code"`
		Msg   string `json:"msg"`
		ReqID string `json:"req_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if envelope.Code != 50000 || envelope.Msg != "internal server error" {
		t.Fatalf("envelope = %+v, want 50000/internal server error", envelope)
	}
	if envelope.ReqID != "test-trace-001" {
		t.Fatalf("req_id = %q, want test-trace-001", envelope.ReqID)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	logs := string(mustReadFile(t, infoPath))
	if !strings.Contains(logs, `"msg":"http access"`) || !strings.Contains(logs, `"status":500`) {
		t.Fatalf("access log missing the recovered 500: %s", logs)
	}
	if !strings.Contains(logs, `"req_id":"test-trace-001"`) {
		t.Fatalf("access log lost req_id: %s", logs)
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
		// The custom config applies literally: only the module route is skipped, the
		// default health-check skip no longer holds.
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

// TestAccessLogUsesSeparateOutputs proves access_outputs separates HTTP access
// entries from runtime logs: 2xx/5xx access entries and the middleware's own
// Warn/Error levels land in the access file, while Info/Error runtime entries
// stay in the application and error files.
// TestAccessLogUsesSeparateOutputs 证明 access_outputs 把 HTTP 访问日志与运行态
// 日志分开：2xx/5xx 访问条目及中间件自身的 Warn/Error 落在访问文件，运行态的
// Info/Error 仍写入应用与错误文件。
func TestAccessLogUsesSeparateOutputs(t *testing.T) {
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "http", "info.log")
	errorPath := filepath.Join(dir, "http", "error.log")
	accessPath := filepath.Join(dir, "access", "access.log")

	module := &testModule{
		name: "catalog",
		reg: func(r *support.Router) error {
			r.GET("/ok", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			r.GET("/fail", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })
			return nil
		},
	}
	app, err := gofy.New(
		gofy.WithName("access-log-split"),
		gofy.WithPIDFile(""),
		gofy.WithLogConfig(logger.Config{
			Outputs:       []string{infoPath},
			ErrorOutputs:  []string{errorPath},
			AccessOutputs: []string{accessPath},
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
	app.Logger().Info("runtime info")
	app.Logger().Error("runtime error")

	for _, path := range []string{"/catalog/ok", "/catalog/fail"} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	infoLog := string(mustReadFile(t, infoPath))
	if !strings.Contains(infoLog, "runtime info") || strings.Contains(infoLog, "http access") {
		t.Fatalf("runtime info log = %q", infoLog)
	}
	errorLog := string(mustReadFile(t, errorPath))
	if !strings.Contains(errorLog, "runtime error") || strings.Contains(errorLog, "http access") {
		t.Fatalf("runtime error log = %q", errorLog)
	}
	accessLog := string(mustReadFile(t, accessPath))
	if strings.Count(accessLog, `"msg":"http access"`) != 2 {
		t.Fatalf("access log = %q", accessLog)
	}
	if !strings.Contains(accessLog, `"status":204`) || !strings.Contains(accessLog, `"status":500`) {
		t.Fatalf("access log lost statuses: %q", accessLog)
	}
	if strings.Contains(accessLog, "runtime") {
		t.Fatalf("access log captured runtime entries: %q", accessLog)
	}
}

// TestReloadRebuildsAccessLogger proves Reload replaces the access logger with
// the new outputs: after the swap, access entries go to the new file and the
// old file stops growing, while runtime logs follow their own config.
// TestReloadRebuildsAccessLogger 证明 Reload 会按新配置替换访问 logger：换代后
// 访问条目写入新文件、旧文件不再增长，运行态日志则跟随各自的配置。
func TestReloadRebuildsAccessLogger(t *testing.T) {
	port := freePort(t)
	dir := t.TempDir()
	firstInfo := filepath.Join(dir, "first", "info.log")
	secondInfo := filepath.Join(dir, "second", "info.log")
	secondAccess := filepath.Join(dir, "access", "access.log")

	app, err := gofy.New(
		gofy.WithName("reload-access-log"),
		withTestHTTP(t, port),
		gofy.WithHealthEndpoints(true),
		gofy.WithModuleFactories(reloadFactory("v1", &recorder{})),
		gofy.WithShutdownTimeout(2*time.Second),
		gofy.WithLogConfig(logger.Config{Outputs: []string{firstInfo}}),
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
		Log: &logger.Config{Outputs: []string{secondInfo}, AccessOutputs: []string{secondAccess}},
	})
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := getText(t, port, "/reload/ping"); got != "v1" {
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

	oldLog := string(mustReadFile(t, firstInfo))
	if strings.Count(oldLog, `"msg":"http access"`) != 1 {
		t.Fatalf("old logger still received access entries: %q", oldLog)
	}
	newLog := string(mustReadFile(t, secondInfo))
	if !strings.Contains(newLog, "application reloaded") || strings.Contains(newLog, "http access") {
		t.Fatalf("new runtime log = %q", newLog)
	}
	accessLog := string(mustReadFile(t, secondAccess))
	if !strings.Contains(accessLog, `"msg":"http access"`) || !strings.Contains(accessLog, "/reload/ping") {
		t.Fatalf("new access log = %q", accessLog)
	}
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

	pid, err := pidfile.Read(pidPath)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	if pid != os.Getpid() {
		t.Fatalf("pid file = %d, want %d", pid, os.Getpid())
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

	pid, err := pidfile.Read(pidPath)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	if pid != os.Getpid() {
		t.Fatalf("pid file = %d, want %d", pid, os.Getpid())
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

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })

	var execErr error
	output := captureStderr(t, func() {
		os.Args = []string{"cli-test", "script", "demo", "order-id=1222"}
		execErr = app.Execute(context.Background())
	})

	if !errors.Is(execErr, gofy.ErrUsage) {
		t.Fatalf("Execute error = %v, want ErrUsage", execErr)
	}
	if code := gofy.ExitCode(execErr); code != gofy.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, gofy.ExitUsage)
	}
	if !strings.Contains(output, "leading --") || !strings.Contains(output, "--order-id=1222") {
		t.Fatalf("stderr = %q, want a leading -- hint", output)
	}
}

// TestExecuteUnknownWorkerGroupReportsToStderr covers the failure that used to be
// silent: the group is looked up before any logger exists, so app.Logger() in main
// is a Nop and the exit code was the only trace. The message has to name the group.
// TestExecuteUnknownWorkerGroupReportsToStderr 覆盖原先静默的那类失败：分组在 logger
// 建立之前就被查表，main 里的 app.Logger() 还是 Nop，只剩退出码可看。错误必须点出分组名。
func TestExecuteUnknownWorkerGroupReportsToStderr(t *testing.T) {
	group, err := gofyworker.NewGroup("group1", gofyworker.Func("only", 1, func(context.Context) error {
		return nil
	}))
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	app, err := gofy.New(
		gofy.WithName("cli-test"),
		gofy.WithWorkerGroups(group),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })

	var execErr error
	output := captureStderr(t, func() {
		os.Args = []string{"cli-test", "worker", "--group=nope"}
		execErr = app.Execute(context.Background())
	})

	if !errors.Is(execErr, gofy.ErrWorkerGroupNotFound) {
		t.Fatalf("Execute error = %v, want ErrWorkerGroupNotFound", execErr)
	}
	if code := gofy.ExitCode(execErr); code != gofy.ExitError {
		t.Fatalf("exit code = %d, want %d", code, gofy.ExitError)
	}
	if !strings.Contains(output, "worker group not found") || !strings.Contains(output, "nope") {
		t.Fatalf("stderr = %q, want the group name in the message", output)
	}
}

// captureStderr runs fn with os.Stderr pointed at a pipe and returns what fn wrote.
// captureStderr 把 os.Stderr 指向管道后执行 fn，返回 fn 写入的内容。
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = old })
	fn()
	os.Stderr = old
	if err := w.Close(); err != nil {
		t.Fatalf("close stderr pipe: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close stderr reader: %v", err)
	}
	return string(out)
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
	// 非法的 spec 必须让正在运行的路由保持原样。
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

// TestReloadDrainsInFlightRequests proves the old generation's modules stop only
// after in-flight requests release their lease, while the new routes already serve
// traffic during the drain.
// TestReloadDrainsInFlightRequests 证明旧代模块要等在途请求释放租约后才停止，
// 而新代路由在排空期间已可对外服务。
func TestReloadDrainsInFlightRequests(t *testing.T) {
	port := freePort(t)
	events := &recorder{}
	entered := make(chan struct{})
	release := make(chan struct{})
	slow := func(*gofy.Runtime) (support.Module, error) {
		return &testModule{
			name:  "drain",
			order: events,
			reg: func(r *support.Router) error {
				r.GET("/slow", func(c *gin.Context) {
					close(entered)
					<-release
					events.add("finish:slow")
					c.String(http.StatusOK, "old")
				})
				return nil
			},
		}, nil
	}
	app, err := gofy.New(
		gofy.WithName("reload-drain"),
		withTestHTTP(t, port),
		gofy.WithHealthEndpoints(true),
		gofy.WithModuleFactories(slow),
		gofy.WithShutdownTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(context.Background()) }()
	waitForServer(t, port)

	respCh := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/drain/slow")
		if err != nil {
			respCh <- "error: " + err.Error()
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		respCh <- string(body)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached the handler")
	}

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- app.Reload(context.Background(), gofy.ReloadSpec{
			ModuleFactories: []gofy.ModuleFactory{reloadFactory("v2", events)},
		})
	}()

	// The successor is published before the drain, so new requests are served
	// by it while the old request is still running.
	// 新 generation 在排空之前就已发布，因此旧请求仍在执行时新请求已经由它处理。
	deadline := time.Now().Add(5 * time.Second)
	for {
		if getText(t, port, "/reload/ping") == "v2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new routes were not published while the old request was in flight")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-reloadDone:
		t.Fatalf("Reload returned before the in-flight request drained: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if got := countEvents(events, "stop:drain"); got != 0 {
		t.Fatalf("old module stopped while a request was in flight (count = %d)", got)
	}

	close(release)
	if got := <-respCh; got != "old" {
		t.Fatalf("in-flight response = %q, want old", got)
	}
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("Reload: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Reload did not finish after the request drained")
	}
	if got := countEvents(events, "stop:drain"); got != 1 {
		t.Fatalf("old module stop count after drain = %d, want 1", got)
	}
	// The old module must stop after the in-flight handler returns, otherwise the old
	// request would keep running against closed resources.
	// 旧模块 Stop 必须晚于在途 handler 返回，否则旧请求会在资源关闭后继续执行。
	finishAt, stopAt := -1, -1
	for i, event := range events.snapshot() {
		switch event {
		case "finish:slow":
			finishAt = i
		case "stop:drain":
			stopAt = i
		}
	}
	if finishAt < 0 || stopAt < 0 || finishAt > stopAt {
		t.Fatalf("event order = %v, want finish:slow before stop:drain", events.snapshot())
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

// TestReloadStartFailureKeepsOldGeneration proves the old generation keeps serving
// when a new module fails to start, and the failed generation neither enters the
// started list nor triggers Stop.
// TestReloadStartFailureKeepsOldGeneration 证明新代模块启动失败时旧代继续服务，
// 失败的新代不会进入已启动列表，也不会触发 Stop。
func TestReloadStartFailureKeepsOldGeneration(t *testing.T) {
	port := freePort(t)
	events := &recorder{}
	app, err := gofy.New(
		gofy.WithName("reload-start-failure"),
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

	bad := func(*gofy.Runtime) (support.Module, error) {
		return &testModule{
			name:  "reload",
			order: events,
			start: errors.New("module boom"),
			reg: func(r *support.Router) error {
				r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "v2") })
				return nil
			},
		}, nil
	}
	if err := app.Reload(context.Background(), gofy.ReloadSpec{
		ModuleFactories: []gofy.ModuleFactory{bad},
	}); err == nil || !strings.Contains(err.Error(), "module boom") {
		t.Fatalf("Reload error = %v, want module start failure", err)
	}
	if got := getText(t, port, "/reload/ping"); got != "v1" {
		t.Fatalf("old generation stopped serving after a failed reload: %q", got)
	}
	if got := countEvents(events, "stop:reload"); got != 0 {
		t.Fatalf("failed generation was stopped %d times, want 0", got)
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
	if got := countEvents(events, "stop:reload"); got != 1 {
		t.Fatalf("old module stop count = %d, want 1", got)
	}
}

// TestReloadAndShutdownSerialize parks Reload inside the new generation's Start and
// triggers Shutdown concurrently: cleanup must wait for Reload to finish before
// stopping modules, and each generation stops exactly once.
// TestReloadAndShutdownSerialize 让 Reload 停在新代 Start 中，再并发触发
// Shutdown：cleanup 必须等 Reload 完成后才停模块，新旧两代各 Stop 一次。
func TestReloadAndShutdownSerialize(t *testing.T) {
	port := freePort(t)
	events := &recorder{}
	v1 := func(*gofy.Runtime) (support.Module, error) {
		return &testModule{
			name:  "reload-v1",
			order: events,
			reg: func(r *support.Router) error {
				r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "v1") })
				return nil
			},
		}, nil
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	v2 := func(*gofy.Runtime) (support.Module, error) {
		return &blockingStartModule{
			testModule: &testModule{
				name:  "reload-v2",
				order: events,
				reg: func(r *support.Router) error {
					r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "v2") })
					return nil
				},
			},
			entered: entered,
			release: release,
		}, nil
	}
	app, err := gofy.New(
		gofy.WithName("reload-shutdown-race"),
		withTestHTTP(t, port),
		gofy.WithHealthEndpoints(true),
		gofy.WithModuleFactories(v1),
		gofy.WithShutdownTimeout(2*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(context.Background()) }()
	waitForServer(t, port)

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- app.Reload(context.Background(), gofy.ReloadSpec{
			ModuleFactories: []gofy.ModuleFactory{v2},
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reload never entered the new module Start")
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- app.Shutdown(context.Background()) }()

	// Shutdown now waits for Reload to release reloadMu; once released both generations
	// stop once, a duplicate Stop is not allowed.
	// Shutdown 现在等 Reload 释放 reloadMu；放行后两代各停一次，不允许重复 Stop。
	close(release)
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("Reload: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Reload did not finish")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not finish")
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}

	if got := countEvents(events, "stop:reload-v1"); got != 1 {
		t.Fatalf("old module stop count = %d, want 1", got)
	}
	if got := countEvents(events, "stop:reload-v2"); got != 1 {
		t.Fatalf("new module stop count = %d, want 1", got)
	}
}

// TestReloadRebuildsMiddlewareRegistry proves the registry is assembled per run by
// the App: modules only resolve aliases by name, so once Reload receives a new
// registrar the new routes immediately use the new instance.
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

// shutdownContextModule records whether Runtime.Context() was already cancelled
// when Stop ran. Background goroutines watch that context, so cleanup must
// cancel it before module IO, the database and the logger are torn down.
// shutdownContextModule 记录 Stop 执行时 Runtime.Context() 是否已经取消。后台 goroutine
// 监听的就是这个 context，因此清理必须在模块 IO、数据库与日志拆除之前取消它。
type shutdownContextModule struct {
	rt             *core.Runtime
	canceledOnStop bool
}

func (m *shutdownContextModule) Name() string                   { return "shutdown-context" }
func (m *shutdownContextModule) Register(*support.Router) error { return nil }
func (m *shutdownContextModule) Start(context.Context) error    { return nil }

func (m *shutdownContextModule) Stop(context.Context) error {
	select {
	case <-m.rt.Context().Done():
		m.canceledOnStop = true
	default:
	}
	return nil
}

// TestShutdownCancelsRunContextBeforeStoppingModules pins A2: modules stop
// after the run context is cancelled, so a module (or any goroutine it owns)
// that waits on Runtime.Context() is released before the resources it uses are
// closed instead of blocking the whole shutdown budget.
// TestShutdownCancelsRunContextBeforeStoppingModules 固定 A2：模块在 run context 取消
// 之后才停止，等待 Runtime.Context() 的模块（或其后台 goroutine）会在依赖的资源关闭之前
// 被释放，而不是卡满整个停机预算。
func TestShutdownCancelsRunContextBeforeStoppingModules(t *testing.T) {
	port := freePort(t)
	var observer *shutdownContextModule

	app, err := gofy.New(
		gofy.WithName("shutdown-cancel-test"),
		withTestHTTP(t, port),
		gofy.WithHealthEndpoints(true),
		gofy.WithModuleFactories(func(rt *core.Runtime) (support.Module, error) {
			observer = &shutdownContextModule{rt: rt}
			return observer, nil
		}),
		gofy.WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runDone := make(chan error, 1)
	go func() { runDone <- app.Run(context.Background()) }()
	waitForServer(t, port)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}

	if observer == nil {
		t.Fatal("module factory never ran")
	}
	if !observer.canceledOnStop {
		t.Fatal("module Stop ran before the run context was cancelled")
	}
}
