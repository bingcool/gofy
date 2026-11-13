package demoserviceclient_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bingcool/gofy-interface-api/client"
	"github.com/yourorg/interface-service/appinterface/demoservice/demoserviceclient"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
)

func TestGetConfigUsesConfigRoute(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"auth_required":false}}`)
	}))
	defer server.Close()

	c, err := demoserviceclient.NewWithBaseURL(server.URL)
	if err != nil {
		t.Fatalf("NewWithBaseURL: %v", err)
	}
	res, err := c.GetConfig(context.Background(), &orderrequest.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if res.AuthRequired {
		t.Fatalf("config = %+v", res)
	}
	if gotPath != "/config" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestGetConfigForwardsCallOptions(t *testing.T) {
	var gotTenant, gotTrace string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get("X-Tenant")
		gotTrace = r.Header.Get("X-Trace")
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"auth_required":false}}`)
	}))
	defer server.Close()

	c, err := demoserviceclient.NewWithBaseURL(server.URL)
	if err != nil {
		t.Fatalf("NewWithBaseURL: %v", err)
	}
	_, err = c.GetConfig(context.Background(), &orderrequest.GetConfigRequest{},
		client.WithHeader("X-Tenant", "tenant-a"),
		client.WithHeaders(http.Header{"X-Trace": {"trace-1"}}),
		client.WithTimeout(2*time.Second),
		client.WithConnectTimeout(time.Second),
	)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if gotTenant != "tenant-a" || gotTrace != "trace-1" {
		t.Fatalf("headers = %q, %q", gotTenant, gotTrace)
	}
}
