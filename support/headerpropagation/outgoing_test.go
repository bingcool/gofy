package headerpropagation

import (
	"context"
	"testing"
)

// TestOutgoingCarriesAllowlistedHeaders pins the producer-side contract: a
// message published inside a request context must carry the caller's trace id,
// and a context without one must not invent headers.
// TestOutgoingCarriesAllowlistedHeaders 固定生产端契约：在请求上下文里发布的消息必须
// 带上调用方 trace id；没有该上下文的发布不得凭空造出 Header。
func TestOutgoingCarriesAllowlistedHeaders(t *testing.T) {
	ctx := ContextWithHeader(context.Background(), HeaderTraceID, "trace-123")
	out := Outgoing(ctx)
	if out[HeaderTraceID] != "trace-123" {
		t.Fatalf("outgoing trace id = %q, want trace-123", out[HeaderTraceID])
	}

	if got := Outgoing(context.Background()); got != nil {
		t.Fatalf("empty context produced headers: %v", got)
	}
}

// TestOutgoingIgnoresNonAllowlistedHeaders keeps the process from forwarding
// headers it never validated on the way in.
// TestOutgoingIgnoresNonAllowlistedHeaders 保证进程不会转发入口没有校验过的 Header。
func TestOutgoingIgnoresNonAllowlistedHeaders(t *testing.T) {
	ctx := ContextWithHeaders(context.Background(), map[string][]string{
		HeaderTraceID: {"trace-1"},
		"X-Internal":  {"secret"},
	})
	out := Outgoing(ctx)
	if _, ok := out["X-Internal"]; ok {
		t.Fatalf("non-allowlisted header forwarded: %v", out)
	}
	if len(out) != 1 {
		t.Fatalf("outgoing headers = %v, want only the trace id", out)
	}
}
