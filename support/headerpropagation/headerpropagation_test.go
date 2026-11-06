package headerpropagation_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/bingcool/gofy/support/headerpropagation"
)

// 公网直连带来的身份头必须丢弃，客户端 IP 改写为直连地址。
func TestCaptureIncomingDropsIdentityFromUntrustedPeer(t *testing.T) {
	in := http.Header{
		"X-Trace-Id":    {"trace-1"},
		"X-User-Id":     {"user-1"},
		"X-User-Code":   {"uc-1"},
		"X-Tenant-Id":   {"tenant-1"},
		"X-User-Name":   {"Alice"},
		"X-Client-Ip":   {"10.9.9.9"},
		"X-User-Agent":  {"browser"},
		"Authorization": {"Bearer leaked"},
		"Cookie":        {"session=1"},
	}

	got := headerpropagation.CaptureIncoming(in, "req-1", nil, "203.0.113.7:5555")

	for _, name := range []string{
		headerpropagation.HeaderUserID,
		headerpropagation.HeaderUserCode,
		headerpropagation.HeaderTenantID,
		headerpropagation.HeaderUserName,
	} {
		if value := got.Get(name); value != "" {
			t.Fatalf("%s = %q, want dropped", name, value)
		}
	}
	if value := got.Get(headerpropagation.HeaderClientIP); value != "203.0.113.7" {
		t.Fatalf("client ip = %q, want direct peer", value)
	}
	if value := got.Get(headerpropagation.HeaderTraceID); value != "trace-1" {
		t.Fatalf("trace id = %q", value)
	}
	if value := got.Get(headerpropagation.HeaderUserAgent); value != "browser" {
		t.Fatalf("user agent = %q", value)
	}
	if got.Get("Authorization") != "" || got.Get("Cookie") != "" {
		t.Fatalf("sensitive headers leaked: %v", got)
	}
}

// 直连地址命中可信 CIDR 时，身份头保留。
func TestCaptureIncomingTrustsConfiguredProxy(t *testing.T) {
	in := http.Header{
		"X-User-Id":   {"user-1"},
		"X-User-Code": {"uc-1"},
		"X-Tenant-Id": {"tenant-1"},
		"X-User-Name": {"Alice"},
		"X-Client-Ip": {"198.51.100.4"},
	}

	got := headerpropagation.CaptureIncoming(in, "", []string{"10.0.0.0/8"}, "10.1.2.3:8080")

	if value := got.Get(headerpropagation.HeaderUserID); value != "user-1" {
		t.Fatalf("user id = %q", value)
	}
	if value := got.Get(headerpropagation.HeaderTenantID); value != "tenant-1" {
		t.Fatalf("tenant id = %q", value)
	}
	if value := got.Get(headerpropagation.HeaderClientIP); value != "198.51.100.4" {
		t.Fatalf("client ip = %q", value)
	}
}

// 可信代理也支持精确 IP 配置。
func TestCaptureIncomingTrustsExactProxyIP(t *testing.T) {
	got := headerpropagation.CaptureIncoming(
		http.Header{"X-User-Id": {"user-1"}},
		"",
		[]string{"10.1.2.3"},
		"10.1.2.3:8080",
	)
	if value := got.Get(headerpropagation.HeaderUserID); value != "user-1" {
		t.Fatalf("user id = %q", value)
	}
}

// 缺少 X-Trace-Id 时用 req_id 兜底，已有透传值时不覆盖。
func TestCaptureIncomingTraceFallback(t *testing.T) {
	got := headerpropagation.CaptureIncoming(http.Header{}, "req-42", nil, "203.0.113.7:5555")
	if value := got.Get(headerpropagation.HeaderTraceID); value != "req-42" {
		t.Fatalf("trace id = %q, want fallback", value)
	}

	got = headerpropagation.CaptureIncoming(
		http.Header{"X-Trace-Id": {"inbound-trace"}},
		"req-42",
		nil,
		"203.0.113.7:5555",
	)
	if value := got.Get(headerpropagation.HeaderTraceID); value != "inbound-trace" {
		t.Fatalf("trace id = %q, want inbound value", value)
	}
}

// context 读写只保留白名单头，getter 返回副本。
func TestContextRoundTrip(t *testing.T) {
	ctx := headerpropagation.ContextWithHeaders(context.Background(), http.Header{
		"x-tenant-id":   {"tenant-1"},
		"Authorization": {"Bearer leaked"},
	})
	ctx = headerpropagation.ContextWithHeader(ctx, "X-User-Id", "user-1")

	headers := headerpropagation.HeadersFromContext(ctx)
	if headers.Get(headerpropagation.HeaderTenantID) != "tenant-1" {
		t.Fatalf("tenant id = %q", headers.Get(headerpropagation.HeaderTenantID))
	}
	if headers.Get(headerpropagation.HeaderUserID) != "user-1" {
		t.Fatalf("user id = %q", headers.Get(headerpropagation.HeaderUserID))
	}
	if headers.Get("Authorization") != "" {
		t.Fatalf("non-whitelisted header stored: %v", headers)
	}
	if got := headerpropagation.HeaderFromContext(ctx, "X-Tenant-Id"); got != "tenant-1" {
		t.Fatalf("header helper = %q", got)
	}

	// getter 返回副本，修改副本不能污染 context。
	headers.Set(headerpropagation.HeaderTenantID, "tenant-2")
	if got := headerpropagation.HeaderFromContext(ctx, headerpropagation.HeaderTenantID); got != "tenant-1" {
		t.Fatalf("context mutated through getter: %q", got)
	}
}

// 空 context 返回空 Header，nil context 也不能 panic。
func TestMissingContext(t *testing.T) {
	if headers := headerpropagation.HeadersFromContext(context.Background()); len(headers) != 0 {
		t.Fatalf("headers = %v", headers)
	}
	if headers := headerpropagation.HeadersFromContext(nil); headers == nil {
		t.Fatal("nil context should return an empty header, not nil")
	}
}
