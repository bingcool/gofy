package middleware_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/jwtauth"
	"github.com/bingcool/gofy/transport"
	"github.com/bingcool/gofy/transport/middleware"
	"github.com/bingcool/gofy/transport/ratelimit"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const testSecret = "middleware-test-secret"

func tokenFor(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func testParser(t *testing.T) *jwtauth.Parser {
	t.Helper()
	p, err := jwtauth.NewParser(jwtauth.Config{Secret: []byte(testSecret)})
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	return p
}

func TestJWTAuthRejectsMissingAndInvalidTokens(t *testing.T) {
	engine := newGin()
	mw, err := middleware.JWTAuth(transport.JWTAuthConfig{Parser: testParser(t)}, nil)
	if err != nil {
		t.Fatalf("JWTAuth: %v", err)
	}
	engine.Use(middleware.RequestID(""))
	engine.Use(mw)
	engine.GET("/private", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, header := range []string{"", "Bearer not-a-token", "Basic abc", "Bearer"} {
		req := httptest.NewRequest(http.MethodGet, "/private", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rr := httptest.NewRecorder()
		engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("header %q status = %d, want 401", header, rr.Code)
		}
		var body struct {
			Code int `json:"code"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Code != 40100 {
			t.Fatalf("header %q code = %d, want 40100", header, body.Code)
		}
		if got := rr.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
			t.Fatalf("WWW-Authenticate = %q", got)
		}
	}
}

func TestJWTAuthStoresClaims(t *testing.T) {
	engine := newGin()
	mw, err := middleware.JWTAuth(transport.JWTAuthConfig{Parser: testParser(t)}, nil)
	if err != nil {
		t.Fatalf("JWTAuth: %v", err)
	}
	engine.Use(mw)
	engine.GET("/private", func(c *gin.Context) {
		claims, ok := transport.JWTClaims(c)
		if !ok {
			c.String(http.StatusInternalServerError, "missing claims")
			return
		}
		c.String(http.StatusOK, claims.Subject)
	})

	req := httptest.NewRequest(http.MethodGet, "/private", nil)
	req.Header.Set("Authorization", "Bearer "+tokenFor(t, map[string]any{
		"sub": "user-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	}))
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "user-1" {
		t.Fatalf("status = %d body = %q", rr.Code, rr.Body.String())
	}
}

func TestJWTAuthOptionalAndSkipPaths(t *testing.T) {
	engine := newGin()
	mw, err := middleware.JWTAuth(transport.JWTAuthConfig{
		Parser:    testParser(t),
		Optional:  true,
		SkipPaths: []string{"/public"},
	}, nil)
	if err != nil {
		t.Fatalf("JWTAuth: %v", err)
	}
	engine.Use(mw)
	handler := func(c *gin.Context) {
		if claims, ok := transport.JWTClaims(c); ok {
			c.String(http.StatusOK, claims.Subject)
			return
		}
		c.String(http.StatusOK, "anonymous")
	}
	engine.GET("/open", handler)
	engine.GET("/public", handler)

	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/open", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "anonymous" {
		t.Fatalf("optional request = %d %q", rr.Code, rr.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	req.Header.Set("Authorization", "Bearer "+tokenFor(t, map[string]any{"sub": "skipped"}))
	rr = httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	if rr.Body.String() != "anonymous" {
		t.Fatalf("skipped path parsed the token: %q", rr.Body.String())
	}
}

type failingStore struct{ err error }

func (s failingStore) Allow(context.Context, string, int, time.Duration) (ratelimit.Result, error) {
	return ratelimit.Result{}, s.err
}

func TestRateLimitRejectsWithHeaders(t *testing.T) {
	engine := newGin()
	mw, err := middleware.RateLimit(transport.RateLimitConfig{
		Store:  ratelimit.NewMemoryStore(),
		Limit:  2,
		Window: time.Minute,
	}, nil)
	if err != nil {
		t.Fatalf("RateLimit: %v", err)
	}
	engine.Use(mw)
	engine.GET("/orders", func(c *gin.Context) { c.Status(http.StatusOK) })

	for i := 0; i < 2; i++ {
		rr := httptest.NewRecorder()
		engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/orders", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, rr.Code)
		}
		if got := rr.Header().Get("X-RateLimit-Limit"); got != "2" {
			t.Fatalf("limit header = %q", got)
		}
		if got := rr.Header().Get("X-RateLimit-Remaining"); got == "" {
			t.Fatal("missing remaining header")
		}
	}

	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/orders", nil))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rr.Code)
	}
	if got := rr.Header().Get("Retry-After"); got == "" || got == "0" {
		t.Fatalf("Retry-After = %q", got)
	}
	if got := rr.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Fatalf("remaining = %q, want 0", got)
	}
	var body struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != 42900 {
		t.Fatalf("code = %d, want 42900", body.Code)
	}
}

func TestRateLimitStoreFailure(t *testing.T) {
	storeErr := errors.New("redis down")

	engine := newGin()
	mw, err := middleware.RateLimit(transport.RateLimitConfig{
		Store: failingStore{err: storeErr}, Limit: 1, Window: time.Minute,
	}, nil)
	if err != nil {
		t.Fatalf("RateLimit: %v", err)
	}
	engine.Use(mw)
	engine.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("fail-closed status = %d, want 503", rr.Code)
	}

	open := newGin()
	mw, err = middleware.RateLimit(transport.RateLimitConfig{
		Store: failingStore{err: storeErr}, Limit: 1, Window: time.Minute, FailOpen: true,
	}, nil)
	if err != nil {
		t.Fatalf("RateLimit: %v", err)
	}
	open.Use(mw)
	open.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	rr = httptest.NewRecorder()
	open.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("fail-open status = %d, want 204", rr.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	engine := newGin()
	mw, err := middleware.SecurityHeaders(transport.SecurityHeadersConfig{
		HSTS:                  24 * time.Hour,
		HSTSIncludeSubdomains: true,
		Custom:                map[string]string{"X-App": "gofy"},
	})
	if err != nil {
		t.Fatalf("SecurityHeaders: %v", err)
	}
	engine.Use(mw)
	engine.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	want := map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
		"Strict-Transport-Security": "max-age=86400; includeSubDomains",
		"X-App":                     "gofy",
	}
	for name, value := range want {
		if got := rr.Header().Get(name); got != value {
			t.Fatalf("%s = %q, want %q", name, got, value)
		}
	}
}

func TestSecurityHeadersValidation(t *testing.T) {
	if _, err := middleware.SecurityHeaders(transport.SecurityHeadersConfig{
		Custom: map[string]string{"X-Bad": "a\r\nInjected: yes"},
	}); err == nil {
		t.Fatal("SecurityHeaders accepted a newline in a header value")
	}
}

func TestAccessLogRedactsSensitiveQuery(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	engine := newGin()
	engine.Use(middleware.AccessLog(zap.New(core), middleware.DefaultAccessLogOptions()))
	engine.GET("/login", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/login?user=alice&password=hunter2&access_token=abc", nil)
	engine.ServeHTTP(httptest.NewRecorder(), req)

	entries := logs.FilterMessage("http access").All()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	query, _ := entries[0].ContextMap()["query"].(string)
	if query == "" {
		t.Fatal("query field is missing")
	}
	if strings.Contains(query, "hunter2") || strings.Contains(query, "abc") {
		t.Fatalf("query leaked a secret: %s", query)
	}
	if !strings.Contains(query, "user=alice") {
		t.Fatalf("query lost a normal value: %s", query)
	}
}

func TestAccessLogMalformedQueryIsOpaque(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	engine := newGin()
	engine.Use(middleware.AccessLog(zap.New(core), middleware.DefaultAccessLogOptions()))
	engine.GET("/search", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/search?q=%zz&password=x", nil)
	engine.ServeHTTP(httptest.NewRecorder(), req)

	entries := logs.FilterMessage("http access").All()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if query, _ := entries[0].ContextMap()["query"].(string); query != "***" {
		t.Fatalf("query = %q, want ***", query)
	}
}
