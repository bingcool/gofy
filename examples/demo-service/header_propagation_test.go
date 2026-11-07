package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/gofytest"
	"github.com/bingcool/gofy/support"
	"gofy/gofy-interface-service/appinterface/demoservice/demoserviceclient"
	"gofy/gofy-interface-service/appinterface/demoservice/orderrequest"
)

type propagationResponse struct {
	Config string `json:"config"`
}

type propagationModule struct {
	reg func(*support.Router) error
}

func (m *propagationModule) Name() string { return "propagate" }

func (m *propagationModule) Register(r *support.Router) error { return m.reg(r) }

// TestHeaderPropagationReachesDownstreamService 串联两个仓库：
// gofy 把入口请求头写入 request context，interface-service 客户端
// 读取同一个 context key 并转发到下游服务。
func TestHeaderPropagationReachesDownstreamService(t *testing.T) {
	var seen http.Header
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"payment_timeout":"tenant-from-header","auth_required":false}}`)
	}))
	defer downstream.Close()

	apiClient, err := demoserviceclient.NewWithBaseURL(downstream.URL)
	if err != nil {
		t.Fatalf("NewWithBaseURL: %v", err)
	}

	module := &propagationModule{
		reg: func(r *support.Router) error {
			support.GET(r, "", func(ctx context.Context, _ *struct{}) (*propagationResponse, error) {
				res, err := apiClient.GetConfig(ctx, &orderrequest.GetConfigRequest{})
				if err != nil {
					return nil, err
				}
				return &propagationResponse{Config: res.PaymentTimeout}, nil
			})
			return nil
		},
	}

	app := gofytest.New(t,
		gofy.WithHeaderPropagation(true),
		gofy.WithTrustedProxies("192.0.2.0/24"),
		gofy.WithModules(module),
	)

	rr := app.GET("/api/v1/propagate",
		"X-User-Id", "user-1",
		"X-Tenant-Id", "tenant-1",
		"X-Trace-Id", "trace-1",
	)
	gofytest.Status(t, rr, http.StatusOK)
	body := gofytest.JSON[propagationResponse](t, rr)
	if body.Config != "tenant-from-header" {
		t.Fatalf("downstream saw tenant %q", body.Config)
	}
	if seen.Get("X-Tenant-Id") != "tenant-1" || seen.Get("X-User-Id") != "user-1" {
		t.Fatalf("identity headers not forwarded: %v", seen)
	}
	if seen.Get("X-Trace-Id") != "trace-1" {
		t.Fatalf("trace id = %q", seen.Get("X-Trace-Id"))
	}
	if seen.Get("X-User-Agent") == "" {
		t.Fatal("X-User-Agent marker missing")
	}
}
