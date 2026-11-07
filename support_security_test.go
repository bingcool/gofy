package gofy_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/gofytest"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/jwtauth"
	"github.com/bingcool/gofy/support/ratelimit"
)

func appToken(t *testing.T) string {
	t.Helper()
	secret := []byte("app-integration-secret")
	header, err := json.Marshal(map[string]any{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"sub": "user-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestAppFallbacksAndSecurityOptions(t *testing.T) {
	parser, err := jwtauth.NewParser(jwtauth.Config{Secret: []byte("app-integration-secret")})
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	module := &testModule{
		name: "secure",
		reg: func(r *support.Router) error {
			support.GET(r, "", func(context.Context, *struct{}) (*orderResponse, error) {
				return &orderResponse{ID: 1}, nil
			})
			return nil
		},
	}
	app := gofytest.New(t,
		gofy.WithName("secure-test"),
		gofy.WithHealthEndpoints(true),
		gofy.WithSecurityHeaders(support.SecurityHeadersConfig{}),
		gofy.WithJWTAuth(support.JWTAuthConfig{Parser: parser}),
		gofy.WithRateLimit(support.RateLimitConfig{
			Store:  ratelimit.NewMemoryStore(),
			Limit:  2,
			Window: time.Minute,
		}),
		gofy.WithModules(module),
	)

	rr := app.GET("/api/v1/nope")
	gofytest.Status(t, rr, http.StatusNotFound)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(40400) {
		t.Fatalf("unknown route code = %v, want 40400", code)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("404 is missing security headers: %q", got)
	}
	if got := rr.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("HSTS is set without HTTPS configuration: %q", got)
	}

	rr = app.POSTJSON("/api/v1/secure", nil)
	gofytest.Status(t, rr, http.StatusMethodNotAllowed)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(40500) {
		t.Fatalf("wrong method code = %v, want 40500", code)
	}
	if allow := rr.Header().Get("Allow"); allow != "GET" {
		t.Fatalf("Allow = %q, want GET", allow)
	}

	rr = app.GET("/healthz")
	gofytest.Status(t, rr, http.StatusOK)

	// The unauthenticated attempt is limited too: it sits in the same bucket.
	rr = app.GET("/api/v1/secure")
	gofytest.Status(t, rr, http.StatusUnauthorized)

	token := appToken(t)
	rr = app.GET("/api/v1/secure", "Authorization", "Bearer "+token)
	gofytest.Status(t, rr, http.StatusOK)
	if got := rr.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Fatalf("remaining = %q, want 0", got)
	}

	rr = app.GET("/api/v1/secure", "Authorization", "Bearer "+token)
	gofytest.Status(t, rr, http.StatusTooManyRequests)
	if got := rr.Header().Get("Retry-After"); got == "" {
		t.Fatal("429 is missing Retry-After")
	}
}

func TestSecurityOptionsAreValidatedAtNew(t *testing.T) {
	if _, err := gofy.New(gofy.WithJWTAuth(support.JWTAuthConfig{})); err == nil {
		t.Fatal("New accepted JWT auth without a parser")
	}
	if _, err := gofy.New(gofy.WithRateLimit(support.RateLimitConfig{Limit: 1, Window: time.Second})); err == nil {
		t.Fatal("New accepted a rate limit without a store")
	}
	if _, err := gofy.New(gofy.WithSecurityHeaders(support.SecurityHeadersConfig{
		Custom: map[string]string{"X-Bad": "a\r\nb"},
	})); err == nil {
		t.Fatal("New accepted a header value with a newline")
	}
}
