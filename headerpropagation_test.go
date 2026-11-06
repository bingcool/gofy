package gofy_test

import (
	"context"
	"net/http"

	"testing"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/gofytest"
	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/bingcool/gofy/transport"
)

type headersResponse struct {
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
	ClientIP string `json:"client_ip"`
	TraceID  string `json:"trace_id"`
}

func propagationModule(t *testing.T) *testModule {
	t.Helper()
	return &testModule{
		name: "propagate",
		reg: func(r *transport.Router) error {
			transport.GET(r, "", func(ctx context.Context, _ *struct{}) (*headersResponse, error) {
				headers := headerpropagation.HeadersFromContext(ctx)
				return &headersResponse{
					UserID:   headers.Get(headerpropagation.HeaderUserID),
					TenantID: headers.Get(headerpropagation.HeaderTenantID),
					ClientIP: headers.Get(headerpropagation.HeaderClientIP),
					TraceID:  headers.Get(headerpropagation.HeaderTraceID),
				}, nil
			})
			return nil
		},
	}
}

// 未配置可信代理时，入口身份头被丢弃，客户端 IP 使用直连地址。
func TestAppHeaderPropagationDropsIdentityFromUntrustedPeer(t *testing.T) {
	app := gofytest.New(t,
		gofy.WithHeaderPropagation(true),
		gofy.WithModules(propagationModule(t)),
	)

	rr := app.GET("/api/v1/propagate",
		"X-User-Id", "user-1",
		"X-Tenant-Id", "tenant-1",
		"X-Client-Ip", "10.9.9.9",
		"X-Trace-Id", "trace-1",
	)
	gofytest.Status(t, rr, http.StatusOK)
	body := gofytest.JSON[headersResponse](t, rr)
	if body.UserID != "" || body.TenantID != "" {
		t.Fatalf("identity headers leaked: %+v", body)
	}
	if body.ClientIP != "192.0.2.1" {
		t.Fatalf("client ip = %q", body.ClientIP)
	}
	if body.TraceID != "trace-1" {
		t.Fatalf("trace id = %q", body.TraceID)
	}
}

// 命中 WithTrustedProxies 的请求保留身份头，缺少 trace 时自动生成。
func TestAppHeaderPropagationTrustsProxy(t *testing.T) {
	app := gofytest.New(t,
		gofy.WithHeaderPropagation(true),
		gofy.WithTrustedProxies("192.0.2.0/24"),
		gofy.WithModules(propagationModule(t)),
	)

	rr := app.GET("/api/v1/propagate",
		"X-User-Id", "user-1",
		"X-Tenant-Id", "tenant-1",
		"X-Client-Ip", "198.51.100.4",
	)
	gofytest.Status(t, rr, http.StatusOK)
	body := gofytest.JSON[headersResponse](t, rr)
	if body.UserID != "user-1" || body.TenantID != "tenant-1" {
		t.Fatalf("identity headers = %+v", body)
	}
	if body.ClientIP != "198.51.100.4" {
		t.Fatalf("client ip = %q", body.ClientIP)
	}
	if body.TraceID == "" {
		t.Fatal("trace id was not generated")
	}
}

// 未启用 WithHeaderPropagation 时不采集任何请求头。
func TestAppHeaderPropagationIsOptIn(t *testing.T) {
	app := gofytest.New(t, gofy.WithModules(propagationModule(t)))

	rr := app.GET("/api/v1/propagate", "X-User-Id", "user-1")
	gofytest.Status(t, rr, http.StatusOK)
	body := gofytest.JSON[headersResponse](t, rr)
	if body.UserID != "" || body.TraceID != "" {
		t.Fatalf("headers captured without the option: %+v", body)
	}
}
