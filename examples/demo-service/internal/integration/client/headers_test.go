package client_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bingcool/gofy-interface-api/client"
	"github.com/bingcool/gofy-interface-api/contracts/common/endpoint"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderroute"
)

func TestCallForwardsPropagatedHeaders(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"order":{"id":"1"}}}`)
	}))
	defer server.Close()

	ctx := client.ContextWithHeaders(context.Background(), http.Header{
		"x-trace-id":    {"trace-1"},
		"X-User-Id":     {"user-1"},
		"X-User-Code":   {"uc-1"},
		"X-Tenant-Id":   {"tenant-1"},
		"X-User-Name":   {"Alice"},
		"X-Client-Ip":   {"198.51.100.4"},
		"Authorization": {"Bearer leaked"},
		"Cookie":        {"session=1"},
	})

	c := newClient(t, server.URL, client.Config{})
	if _, err := client.Call(ctx, c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 1}); err != nil {
		t.Fatalf("Call: %v", err)
	}

	for name, want := range map[string]string{
		client.HeaderTraceID:  "trace-1",
		client.HeaderUserID:   "user-1",
		client.HeaderUserCode: "uc-1",
		client.HeaderTenantID: "tenant-1",
		client.HeaderUserName: "Alice",
		client.HeaderClientIP: "198.51.100.4",
	} {
		if value := got.Get(name); value != want {
			t.Fatalf("%s = %q, want %q", name, value, want)
		}
	}
	if value := got.Get("Authorization"); value != "" {
		t.Fatalf("Authorization leaked: %q", value)
	}
	if value := got.Get("Cookie"); value != "" {
		t.Fatalf("Cookie leaked: %q", value)
	}
	if value := got.Get(client.HeaderUserAgent); value != client.DefaultPropagatedUserAgent {
		t.Fatalf("X-User-Agent = %q", value)
	}
}

var headerRoute = endpoint.New[headerRequest, orderresponse.GetOrderResponse](endpoint.Spec{
	OperationID: "HeaderEcho",
	Method:      http.MethodGet,
	Path:        "/header-echo",
})

type headerRequest struct {
	Tenant string `header:"X-Tenant-Id"`
}

func TestCallHeaderMergeOrder(t *testing.T) {
	var gotTenant string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get(client.HeaderTenantID)
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"order":{"id":"1"}}}`)
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{})
	ctx := client.ContextWithHeader(context.Background(), client.HeaderTenantID, "from-context")

	if _, err := client.Call(ctx, c, headerRoute, &headerRequest{Tenant: "from-struct"}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if gotTenant != "from-struct" {
		t.Fatalf("request struct should override propagated header: %q", gotTenant)
	}

	if _, err := client.Call(ctx, c, headerRoute, &headerRequest{Tenant: "from-struct"},
		client.WithHeader(client.HeaderTenantID, "from-option")); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if gotTenant != "from-option" {
		t.Fatalf("call option should override propagated header: %q", gotTenant)
	}
}

func TestCallUsesPropagatedUserAgentWhenPresent(t *testing.T) {
	var gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get(client.HeaderUserAgent)
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"order":{"id":"1"}}}`)
	}))
	defer server.Close()

	c := newClient(t, server.URL, client.Config{})
	ctx := client.ContextWithHeader(context.Background(), client.HeaderUserAgent, "gateway")
	if _, err := client.Call(ctx, c, orderroute.GetOrder, &orderrequest.GetOrderRequest{ID: 1}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if gotUserAgent != "gateway" {
		t.Fatalf("X-User-Agent = %q", gotUserAgent)
	}
}

func TestHeaderContextFiltersAndCopies(t *testing.T) {
	ctx := client.ContextWithHeaders(context.Background(), http.Header{
		"x-tenant-id":   {"tenant-1"},
		"Authorization": {"Bearer leaked"},
	})
	headers := client.HeadersFromContext(ctx)
	if headers.Get(client.HeaderTenantID) != "tenant-1" {
		t.Fatalf("tenant = %q", headers.Get(client.HeaderTenantID))
	}
	if headers.Get("Authorization") != "" {
		t.Fatalf("non-whitelisted header stored: %v", headers)
	}

	headers.Set(client.HeaderTenantID, "tenant-2")
	if got := client.HeaderFromContext(ctx, client.HeaderTenantID); got != "tenant-1" {
		t.Fatalf("context mutated through getter: %q", got)
	}
}
