package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/bingcool/gofy/support/middleware"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func newGin() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

func TestRequestIDReusesValidHeader(t *testing.T) {
	engine := newGin()
	engine.Use(middleware.RequestID(support.DefaultRequestIDHeader))
	engine.GET("/", func(c *gin.Context) {
		trace := headerpropagation.HeaderFromContext(c.Request.Context(), headerpropagation.HeaderTraceID)
		c.String(http.StatusOK, c.GetString(support.RequestIDKey)+"|"+
			support.RequestIDFromContext(c.Request.Context())+"|"+trace)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(support.DefaultRequestIDHeader, "abc-123.valid_id")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	want := "abc-123.valid_id|abc-123.valid_id|abc-123.valid_id"
	if got := rr.Body.String(); got != want {
		t.Fatalf("gin req_id|context req_id|propagated trace = %q, want %q", got, want)
	}
	if got := rr.Header().Get(support.DefaultRequestIDHeader); got != "abc-123.valid_id" {
		t.Fatalf("response header = %q, want header value", got)
	}
}

func TestRequestIDReplacesInvalidHeader(t *testing.T) {
	engine := newGin()
	engine.Use(middleware.RequestID(""))
	engine.GET("/", func(c *gin.Context) {
		trace := headerpropagation.HeaderFromContext(c.Request.Context(), headerpropagation.HeaderTraceID)
		if support.RequestIDFromContext(c.Request.Context()) != c.GetString(support.RequestIDKey) || trace != c.GetString(support.RequestIDKey) {
			c.String(http.StatusInternalServerError, "request id was not propagated")
			return
		}
		c.String(http.StatusOK, c.GetString(support.RequestIDKey))
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(support.DefaultRequestIDHeader, "invalid id\twith spaces")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	got := rr.Body.String()
	if got == "" || got == "invalid id\twith spaces" {
		t.Fatalf("context id = %q, want generated UUID", got)
	}
	if len(got) != 36 {
		t.Fatalf("generated id = %q, want a UUID", got)
	}
	if h := rr.Header().Get(support.DefaultRequestIDHeader); h != got {
		t.Fatalf("response header = %q, want %q", h, got)
	}
}

func TestRequestIDPrefersUpstreamTraceWithCustomHeader(t *testing.T) {
	engine := newGin()
	engine.Use(middleware.RequestID("X-Custom-Request-ID"))
	engine.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString(support.RequestIDKey)+"|"+
			headerpropagation.HeaderFromContext(c.Request.Context(), headerpropagation.HeaderTraceID))
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(support.DefaultRequestIDHeader, "upstream-trace")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if got := rr.Body.String(); got != "upstream-trace|upstream-trace" {
		t.Fatalf("request id|propagated = %q, want the upstream trace", got)
	}
}

func TestAccessLogRecordsStructuredFields(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)

	engine := newGin()
	engine.Use(middleware.RequestID(""))
	engine.Use(middleware.AccessLog(logger, middleware.DefaultAccessLogOptions()))
	engine.GET("/orders", func(c *gin.Context) { c.Status(http.StatusCreated) })
	engine.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/orders?x=1", nil)
	req.Header.Set("User-Agent", "test-agent")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	entries := logs.FilterMessage("http access").All()
	if len(entries) != 1 {
		t.Fatalf("access log entries = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	for _, key := range []string{"method", "path", "status", "bytes", "latency_ms", "req_id", "client_ip", "user_agent"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("access log is missing %q: %v", key, fields)
		}
	}
	if fields["status"] != int64(http.StatusCreated) {
		t.Fatalf("status = %v, want 201", fields["status"])
	}
	if fields["path"] != "/orders" {
		t.Fatalf("path = %v, want /orders", fields["path"])
	}

	// A path matching SkipPaths must not produce any log entry. healthz is
	// requested once here, so the entry count stays at 1 (the normal request
	// above).
	// 命中 SkipPaths 的路径不能产生任何日志。这里 healthz 只请求一次，
	// 期望日志条数保持为 1（即上面的普通请求）。
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got := logs.FilterMessage("http access").Len(); got != 1 {
		t.Fatalf("skipped path produced an entry, count = %d", got)
	}
}

func TestRecoveryHidesPanicDetails(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	logger := zap.New(core)

	engine := newGin()
	engine.Use(middleware.RequestID(""))
	engine.Use(middleware.Recovery(logger, support.NewResponder(support.ResponderConfig{})))
	engine.GET("/panic", func(*gin.Context) { panic("database password is hunter2") })

	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if body := rr.Body.String(); strings.Contains(body, "hunter2") || strings.Contains(body, "goroutine") || strings.Contains(body, "stack") {
		t.Fatalf("response leaked panic details: %s", body)
	}
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if envelope.Code != 50000 || envelope.Msg != "internal server error" {
		t.Fatalf("envelope = %+v, want 50000/internal server error", envelope)
	}

	entries := logs.FilterMessage("panic recovered").All()
	if len(entries) != 1 {
		t.Fatalf("panic log entries = %d, want 1", len(entries))
	}
	if _, ok := entries[0].ContextMap()["stacktrace"]; !ok {
		t.Fatal("panic log is missing the stacktrace")
	}
}

func TestRecoveryMapsHandledError(t *testing.T) {
	engine := newGin()
	engine.Use(middleware.Recovery(zap.NewNop(), support.NewResponder(support.ResponderConfig{})))
	engine.GET("/abort", func(c *gin.Context) {
		_ = c.Error(apperr.Forbidden("no access"))
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 40300})
	})

	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/abort", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

// TestRecoveryBoundaryIsDownstreamOnly pins the coverage boundary of Recovery:
// it only catches the chain installed after it. A panic in an outer middleware
// keeps propagating to net/http, while the outer AccessLog still records the
// final 500 produced from the inner panic. App assembly order relies on both
// properties.
// TestRecoveryBoundaryIsDownstreamOnly 固定 Recovery 的覆盖边界：它只捕获安装
// 之后的链路。装在外层的中间件 panic 会继续向上抛给 net/http；外层 AccessLog
// 仍能记录内层 panic 转换后的最终 500。App 装配顺序依赖这两个性质。
func TestRecoveryBoundaryIsDownstreamOnly(t *testing.T) {
	t.Run("outer panic escapes", func(t *testing.T) {
		engine := newGin()
		engine.Use(func(*gin.Context) { panic("outer boom") })
		engine.Use(middleware.Recovery(zap.NewNop(), support.NewResponder(support.ResponderConfig{})))
		engine.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

		defer func() {
			if rec := recover(); rec == nil {
				t.Fatal("panic before Recovery was swallowed")
			}
		}()
		engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})

	t.Run("access log records recovered panic", func(t *testing.T) {
		core, logs := observer.New(zapcore.InfoLevel)
		engine := newGin()
		engine.Use(middleware.AccessLog(zap.New(core), middleware.DefaultAccessLogOptions()))
		engine.Use(middleware.Recovery(zap.NewNop(), support.NewResponder(support.ResponderConfig{})))
		engine.GET("/panic", func(*gin.Context) { panic("boom") })

		rr := httptest.NewRecorder()
		engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/panic", nil))
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rr.Code)
		}
		entries := logs.FilterMessage("http access").All()
		if len(entries) != 1 {
			t.Fatalf("access log entries = %d, want 1", len(entries))
		}
		if got, ok := entries[0].ContextMap()["status"]; !ok || got != int64(http.StatusInternalServerError) {
			t.Fatalf("access log status = %v, want 500", got)
		}
	})
}

func TestCORSValidation(t *testing.T) {
	_, err := middleware.CORS(support.CORSConfig{
		AllowOrigins:     []string{"*"},
		AllowCredentials: true,
	})
	if err == nil {
		t.Fatal("CORS accepted credentials with a wildcard origin")
	}

	_, err = middleware.CORS(support.CORSConfig{
		AllowOrigins: []string{"https://example.com"},
		MaxAge:       -1,
	})
	if err == nil {
		t.Fatal("CORS accepted a negative MaxAge")
	}

	mw, err := middleware.CORS(support.CORSConfig{})
	if err != nil || mw != nil {
		t.Fatalf("CORS with no origins = (%v, %v), want (nil, nil)", mw, err)
	}
}

func TestCORSPreflight(t *testing.T) {
	mw, err := middleware.CORS(support.CORSConfig{
		AllowOrigins:     []string{"https://example.com"},
		AllowMethods:     []string{"GET", "POST"},
		AllowHeaders:     []string{"Content-Type"},
		AllowCredentials: true,
	})
	if err != nil {
		t.Fatalf("CORS: %v", err)
	}
	engine := newGin()
	engine.Use(mw)
	engine.POST("/orders", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodOptions, "/orders", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Headers", "X-Custom")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("allow origin = %q, want the request origin", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("allow credentials = %q, want true", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type" {
		t.Fatalf("allow headers = %q, want the configured Content-Type", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST" {
		t.Fatalf("allow methods = %q, want GET, POST", got)
	}
}

func TestCORSWildcardWithoutCredentials(t *testing.T) {
	mw, err := middleware.CORS(support.CORSConfig{AllowOrigins: []string{"*"}})
	if err != nil {
		t.Fatalf("CORS: %v", err)
	}
	engine := newGin()
	engine.Use(mw)
	engine.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://any.example")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("allow origin = %q, want *", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("allow credentials = %q, want empty", got)
	}
}

func TestCORSRejectsDisallowedOrigin(t *testing.T) {
	mw, err := middleware.CORS(support.CORSConfig{AllowOrigins: []string{"https://example.com"}})
	if err != nil {
		t.Fatalf("CORS: %v", err)
	}
	engine := newGin()
	engine.Use(mw)
	engine.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		req := httptest.NewRequest(method, "/", nil)
		req.Header.Set("Origin", "https://evil.example")
		rr := httptest.NewRecorder()
		engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403", method, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "Forbidden") {
			t.Fatalf("%s body = %q", method, rr.Body.String())
		}
	}
}

func TestCORSBareDomainExpandsSchemes(t *testing.T) {
	mw, err := middleware.CORS(support.CORSConfig{AllowOrigins: []string{"example.com"}})
	if err != nil {
		t.Fatalf("CORS: %v", err)
	}
	engine := newGin()
	engine.Use(mw)
	engine.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, origin := range []string{"http://example.com", "https://example.com"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", origin)
		rr := httptest.NewRecorder()
		engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("origin %s status = %d", origin, rr.Code)
		}
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Fatalf("origin %s allow-origin = %q", origin, got)
		}
	}
}

func TestCORSOriginPatterns(t *testing.T) {
	mw, err := middleware.CORS(support.CORSConfig{
		AllowOriginPatterns: []string{"*.example.com"},
		AllowMethods:        []string{"GET"},
	})
	if err != nil {
		t.Fatalf("CORS: %v", err)
	}
	engine := newGin()
	engine.Use(mw)
	engine.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	allowed := httptest.NewRequest(http.MethodGet, "/", nil)
	allowed.Header.Set("Origin", "https://api.example.com")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, allowed)
	if rr.Code != http.StatusOK {
		t.Fatalf("pattern origin status = %d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://api.example.com" {
		t.Fatalf("allow origin = %q", got)
	}

	blocked := httptest.NewRequest(http.MethodGet, "/", nil)
	blocked.Header.Set("Origin", "https://example.org")
	rr = httptest.NewRecorder()
	engine.ServeHTTP(rr, blocked)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("other domain status = %d, want 403", rr.Code)
	}
}

func TestCORSPathWhitelist(t *testing.T) {
	mw, err := middleware.CORS(support.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowPaths:   []string{"/api/*"},
	})
	if err != nil {
		t.Fatalf("CORS: %v", err)
	}
	engine := newGin()
	engine.Use(mw)
	engine.GET("/api/orders", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET("/other", func(c *gin.Context) { c.Status(http.StatusOK) })

	allowed := httptest.NewRequest(http.MethodGet, "/api/orders?page=1", nil)
	allowed.Header.Set("Origin", "https://client.example")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, allowed)
	if rr.Code != http.StatusOK {
		t.Fatalf("allowed path status = %d", rr.Code)
	}

	blocked := httptest.NewRequest(http.MethodGet, "/other", nil)
	blocked.Header.Set("Origin", "https://client.example")
	rr = httptest.NewRecorder()
	engine.ServeHTTP(rr, blocked)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("blocked path status = %d, want 403", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "path=/other") {
		t.Fatalf("blocked body = %q", rr.Body.String())
	}
}

func TestCORSNonPreflightHeaders(t *testing.T) {
	mw, err := middleware.CORS(support.CORSConfig{
		AllowOrigins:  []string{"https://example.com"},
		AllowMethods:  []string{"GET", "POST"},
		AllowHeaders:  []string{"Content-Type"},
		ExposeHeaders: []string{"X-Total"},
		MaxAge:        time.Hour,
	})
	if err != nil {
		t.Fatalf("CORS: %v", err)
	}
	engine := newGin()
	engine.Use(mw)
	engine.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST" {
		t.Fatalf("allow methods = %q", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type" {
		t.Fatalf("allow headers = %q", got)
	}
	if got := rr.Header().Get("Access-Control-Expose-Headers"); got != "X-Total" {
		t.Fatalf("expose headers = %q", got)
	}
	if got := rr.Header().Get("Access-Control-Max-Age"); got != "3600" {
		t.Fatalf("max age = %q", got)
	}
	if got := rr.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("vary = %q", got)
	}
}

func TestCORSOptionsWithoutOrigin(t *testing.T) {
	mw, err := middleware.CORS(support.CORSConfig{AllowOrigins: []string{"*"}})
	if err != nil {
		t.Fatalf("CORS: %v", err)
	}
	engine := newGin()
	engine.Use(mw)
	engine.OPTIONS("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequest(http.MethodOptions, "/", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS without origin = %d, want 204", rr.Code)
	}
}

func TestCORSDefaultMaxAge(t *testing.T) {
	mw, err := middleware.CORS(support.CORSConfig{AllowOrigins: []string{"*"}})
	if err != nil {
		t.Fatalf("CORS: %v", err)
	}
	engine := newGin()
	engine.Use(mw)
	engine.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://any.example")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	if got := rr.Header().Get("Access-Control-Max-Age"); got != "86400" {
		t.Fatalf("default max age = %q, want 86400", got)
	}
}

func TestCORSRejectsInvalidPatterns(t *testing.T) {
	cases := []support.CORSConfig{
		{AllowOrigins: []string{"*"}, AllowOriginPatterns: []string{"https://example.com"}},
		{AllowOrigins: []string{"*"}, AllowOriginPatterns: []string{"example.com:8080"}},
		{AllowOrigins: []string{"*"}, AllowPaths: []string{" "}},
	}
	for _, cfg := range cases {
		if _, err := middleware.CORS(cfg); err == nil {
			t.Fatalf("CORS accepted invalid config: %+v", cfg)
		}
	}
}

// TestAccessLogRejectsNegativeMaxPathLength: a negative cap used to flip the
// truncation test and panic on path[:negative] for every request. It has to be
// reported at construction, not at the first request.
// TestAccessLogRejectsNegativeMaxPathLength：负数上限会让截断判断恒真，并对每个
// 请求执行 path[:负数] 而 panic。必须在构造时报错，而不是等到第一个请求。
func TestAccessLogRejectsNegativeMaxPathLength(t *testing.T) {
	opts := middleware.DefaultAccessLogOptions()
	opts.MaxPathLength = -1
	if err := opts.Validate(); err == nil {
		t.Fatal("Validate accepted a negative max path length")
	}

	// AccessLog cannot return an error, so it must at least clamp instead of
	// panicking when a direct caller skipped Validate.
	// AccessLog 无法返回错误，至少要在直接调用方跳过 Validate 时收敛到默认值而不是 panic。
	core, logs := observer.New(zapcore.InfoLevel)
	engine := newGin()
	engine.Use(middleware.AccessLog(zap.New(core), opts))
	engine.GET("/orders", func(c *gin.Context) { c.Status(http.StatusOK) })

	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/orders", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	entries := logs.FilterMessage("http access").All()
	if len(entries) != 1 {
		t.Fatalf("access log entries = %d, want 1", len(entries))
	}
	if got := entries[0].ContextMap()["path"]; got != "/orders" {
		t.Fatalf("path = %v, want /orders", got)
	}
}

// TestAccessLogReportsZeroBytesWhenNoBodyWasWritten: gin returns -1 from
// Writer.Size() until something is written, and a 204 never writes. Logging -1
// poisons byte-based dashboards.
// TestAccessLogReportsZeroBytesWhenNoBodyWasWritten：gin 的 Writer.Size() 在写入
// 之前返回 -1，而 204 从不写入。记录 -1 会污染按字节聚合的看板。
func TestAccessLogReportsZeroBytesWhenNoBodyWasWritten(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	engine := newGin()
	engine.Use(middleware.AccessLog(zap.New(core), middleware.DefaultAccessLogOptions()))
	engine.GET("/no-content", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/no-content", nil))

	entries := logs.FilterMessage("http access").All()
	if len(entries) != 1 {
		t.Fatalf("access log entries = %d, want 1", len(entries))
	}
	if got := entries[0].ContextMap()["bytes"]; got != int64(0) {
		t.Fatalf("bytes = %v, want 0", got)
	}
}
