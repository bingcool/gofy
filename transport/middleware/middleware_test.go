package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy/transport"
	"github.com/bingcool/gofy/transport/middleware"
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
	engine.Use(middleware.RequestID("X-Request-ID"))
	engine.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString(transport.RequestIDKey))
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "abc-123.valid_id")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if got := rr.Body.String(); got != "abc-123.valid_id" {
		t.Fatalf("context id = %q, want header value", got)
	}
	if got := rr.Header().Get("X-Request-ID"); got != "abc-123.valid_id" {
		t.Fatalf("response header = %q, want header value", got)
	}
}

func TestRequestIDReplacesInvalidHeader(t *testing.T) {
	engine := newGin()
	engine.Use(middleware.RequestID(""))
	engine.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString(transport.RequestIDKey))
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(transport.DefaultRequestIDHeader, "invalid id\twith spaces")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	got := rr.Body.String()
	if got == "" || got == "invalid id\twith spaces" {
		t.Fatalf("context id = %q, want generated UUID", got)
	}
	if len(got) != 36 {
		t.Fatalf("generated id = %q, want a UUID", got)
	}
	if h := rr.Header().Get(transport.DefaultRequestIDHeader); h != got {
		t.Fatalf("response header = %q, want %q", h, got)
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

	// Skipped paths must not produce an entry.
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
	engine.Use(middleware.Recovery(logger, transport.NewResponder(transport.ResponderConfig{})))
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
	engine.Use(middleware.Recovery(zap.NewNop(), transport.NewResponder(transport.ResponderConfig{})))
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

func TestCORSValidation(t *testing.T) {
	_, err := middleware.CORS(transport.CORSConfig{
		AllowOrigins:     []string{"*"},
		AllowCredentials: true,
	})
	if err == nil {
		t.Fatal("CORS accepted credentials with a wildcard origin")
	}

	_, err = middleware.CORS(transport.CORSConfig{
		AllowOrigins: []string{"https://example.com"},
		MaxAge:       -1,
	})
	if err == nil {
		t.Fatal("CORS accepted a negative MaxAge")
	}

	mw, err := middleware.CORS(transport.CORSConfig{})
	if err != nil || mw != nil {
		t.Fatalf("CORS with no origins = (%v, %v), want (nil, nil)", mw, err)
	}
}

func TestCORSPreflight(t *testing.T) {
	mw, err := middleware.CORS(transport.CORSConfig{
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
	if got := rr.Header().Get("Access-Control-Allow-Headers"); got != "X-Custom" {
		t.Fatalf("allow headers = %q, want X-Custom", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST" {
		t.Fatalf("allow methods = %q, want GET, POST", got)
	}
}

func TestCORSWildcardWithoutCredentials(t *testing.T) {
	mw, err := middleware.CORS(transport.CORSConfig{AllowOrigins: []string{"*"}})
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
