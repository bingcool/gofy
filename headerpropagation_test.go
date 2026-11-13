package gofy_test

import (
	"context"
	"net/http"

	"testing"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/gofytest"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/headerpropagation"
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
		reg: func(r *support.Router) error {
			support.GET(r, "", func(ctx context.Context, _ *struct{}) (*headersResponse, error) {
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

// Without a configured trusted proxy the inbound identity headers are dropped and
// the client IP comes from the direct connection.
// 未配置可信代理时，入口身份头被丢弃，客户端 IP 使用直连地址。
func TestAppHeaderPropagationDropsIdentityFromUntrustedPeer(t *testing.T) {
	app := gofytest.New(t,
		gofy.WithHeaderPropagation(true),
		gofy.WithModules(propagationModule(t)),
	)

	rr := app.GET("/propagate",
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

// A request from a proxy registered with WithTrustedProxies keeps its identity
// headers, and a missing trace id is generated.
// 命中 WithTrustedProxies 的请求保留身份头，缺少 trace 时自动生成。
func TestAppHeaderPropagationTrustsProxy(t *testing.T) {
	app := gofytest.New(t,
		gofy.WithHeaderPropagation(true),
		gofy.WithTrustedProxies("192.0.2.0/24"),
		gofy.WithModules(propagationModule(t)),
	)

	rr := app.GET("/propagate",
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

// WithHeaderPropagation disabled means identity headers are not collected;
// RequestID still writes the trace id into the context for downstream clients to
// propagate.
// 未启用 WithHeaderPropagation 时不采集身份头；但 RequestID 仍会把 trace id
// 写入 context，供下游客户端透传。
func TestAppHeaderPropagationIsOptIn(t *testing.T) {
	app := gofytest.New(t, gofy.WithModules(propagationModule(t)))

	rr := app.GET("/propagate", "X-User-Id", "user-1")
	gofytest.Status(t, rr, http.StatusOK)
	body := gofytest.JSON[headersResponse](t, rr)
	if body.UserID != "" || body.TenantID != "" || body.ClientIP != "" {
		t.Fatalf("identity headers captured without the option: %+v", body)
	}
	if body.TraceID == "" {
		t.Fatal("trace id was not propagated by RequestID")
	}
	if got := rr.Header().Get(support.DefaultRequestIDHeader); got != body.TraceID {
		t.Fatalf("trace id = %q, response header = %q", body.TraceID, got)
	}
}
