package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/bingcool/gofy/transport"
	"github.com/bingcool/gofy/transport/middleware"
	"github.com/gin-gonic/gin"
)

func propagatedHandler(c *gin.Context) {
	headers := headerpropagation.HeadersFromContext(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{
		"user_id":   headers.Get(headerpropagation.HeaderUserID),
		"tenant_id": headers.Get(headerpropagation.HeaderTenantID),
		"client_ip": headers.Get(headerpropagation.HeaderClientIP),
		"trace_id":  headers.Get(headerpropagation.HeaderTraceID),
		"auth":      headers.Get("Authorization"),
	})
}

// 不可信对端携带的身份头必须丢弃，Authorization 也不进入透传上下文。
func TestHeaderPropagationDropsIdentityFromUntrustedPeer(t *testing.T) {
	engine := newGin()
	engine.Use(middleware.RequestID(""))
	engine.Use(middleware.HeaderPropagation(nil))
	engine.GET("/", propagatedHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.Header.Set("X-Trace-Id", "trace-1")
	req.Header.Set("X-User-Id", "user-1")
	req.Header.Set("X-Tenant-Id", "tenant-1")
	req.Header.Set("X-Client-Ip", "10.9.9.9")
	req.Header.Set("Authorization", "Bearer leaked")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	body := decodeJSON(t, rr)
	if body["user_id"] != "" || body["tenant_id"] != "" {
		t.Fatalf("identity headers leaked: %v", body)
	}
	if body["client_ip"] != "203.0.113.7" {
		t.Fatalf("client_ip = %v", body["client_ip"])
	}
	if body["trace_id"] != "trace-1" {
		t.Fatalf("trace_id = %v", body["trace_id"])
	}
	if body["auth"] != "" {
		t.Fatalf("authorization leaked: %v", body["auth"])
	}
}

// 可信代理转发的身份头保留，客户端 IP 使用代理提供的原值。
func TestHeaderPropagationKeepsIdentityFromTrustedProxy(t *testing.T) {
	engine := newGin()
	engine.Use(middleware.RequestID(""))
	engine.Use(middleware.HeaderPropagation([]string{"192.0.2.0/24"}))
	engine.GET("/", propagatedHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("X-User-Id", "user-1")
	req.Header.Set("X-Tenant-Id", "tenant-1")
	req.Header.Set("X-Client-Ip", "198.51.100.4")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	body := decodeJSON(t, rr)
	if body["user_id"] != "user-1" || body["tenant_id"] != "tenant-1" {
		t.Fatalf("identity headers = %v", body)
	}
	if body["client_ip"] != "198.51.100.4" {
		t.Fatalf("client_ip = %v", body["client_ip"])
	}
}

// 没有 X-Trace-Id 时，trace id 使用 RequestID 生成的值。
func TestHeaderPropagationUsesRequestIDAsTraceFallback(t *testing.T) {
	engine := newGin()
	engine.Use(middleware.RequestID(""))
	engine.Use(middleware.HeaderPropagation(nil))
	engine.GET("/", propagatedHandler)

	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	body := decodeJSON(t, rr)
	traceID, _ := body["trace_id"].(string)
	if traceID == "" {
		t.Fatal("trace id was not generated")
	}
	if traceID != rr.Header().Get(transport.DefaultRequestIDHeader) {
		t.Fatalf("trace id = %q, request id = %q", traceID, rr.Header().Get(transport.DefaultRequestIDHeader))
	}
}

func decodeJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rr.Body.String(), err)
	}
	return body
}
