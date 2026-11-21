package ai

// Integration scenarios for the provider HTTP path, all against httptest
// servers so they run in every environment: timeout, transient-failure
// retries with an identical request body (idempotency), and the behaviour
// when an SSE connection drops mid-stream.
// 供应商 HTTP 链路的集成场景，全部使用 httptest，任何环境都能跑：超时、
// 瞬时失败重试且请求体逐字节一致（幂等）、以及 SSE 中途断连的行为。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func slowCompatConfig(baseURL string, timeout time.Duration) Config {
	cfg := compatConfig(baseURL)
	// Timeout lives on the model: it bounds one generation against that
	// provider, not the client as a whole.
	// Timeout 在 model 上：它限制对单个供应商的一次生成，而不是整个客户端。
	model := cfg.Models["qwen"]
	model.Timeout = timeout
	cfg.Models["qwen"] = model
	return cfg
}

func TestIntegrationTimeoutCancelsSlowGeneration(t *testing.T) {
	reached := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case reached <- struct{}{}:
		default:
		}
		// Generation takes far longer than the client timeout.
		// 生成的耗时远超客户端超时。
		time.Sleep(5 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"x","choices":[{"message":{"role":"assistant","content":"late"}}]}`)
	}))
	defer srv.Close()

	client := newTestClient(t, slowCompatConfig(srv.URL, 300*time.Millisecond))

	start := time.Now()
	_, err := client.Chat(context.Background(), ChatRequest{Prompt: "hi"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("slow generation must fail with a timeout")
	}
	// The deadline must cut the call at the configured timeout, not at the
	// server's response time.
	// deadline 必须按配置的超时切断调用，而不是等服务端响应。
	if elapsed > 2*time.Second {
		t.Fatalf("timeout took %s to surface, want well below the 5s handler", elapsed)
	}
	select {
	case <-reached:
	default:
		t.Fatal("the request never reached the slow server")
	}
}

func TestIntegrationRetriesTransientFailures(t *testing.T) {
	var calls atomic.Int32
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls.Add(1)
		bodies = append(bodies, string(body))
		switch n := calls.Load(); {
		case n <= 2:
			// Two transient failures, then success: exactly the shape a
			// provider blip has.
			// 两次瞬时失败后成功——正是供应商抖动的形态。
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprint(w, `{"error":{"message":"upstream unavailable","type":"server_error","code":502}}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"chatcmpl-1","object":"chat.completion","model":"qwen-max",`+
				`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
				`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}
	}))
	defer srv.Close()

	cfg := compatConfig(srv.URL)
	retries := 3
	retryModel := cfg.Models["qwen"]
	retryModel.MaxRetries = &retries
	cfg.Models["qwen"] = retryModel
	client := newTestClient(t, cfg)

	resp, err := client.Chat(context.Background(), ChatRequest{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Chat after retries: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("text = %q", resp.Text)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("provider hit %d time(s), want 3 (two failures + one success)", got)
	}
	// Idempotency on the wire: every retry replays the byte-identical request,
	// so a gateway can safely route any attempt.
	// 线上幂等：每次重试逐字节重放同一请求，网关转发任何一次都是安全的。
	for i, body := range bodies {
		if body != bodies[0] {
			t.Fatalf("retry %d body differs from the first:\nfirst: %s\nretry: %s", i, bodies[0], body)
		}
	}
}

func TestIntegrationStreamStopsOnDisconnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: "+sseChunk("Hel")+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// Drop the TCP connection mid-stream. There is no finish_reason and no
		// [DONE]: the only signal the client gets is the broken connection.
		// 流中途掐断 TCP 连接。没有 finish_reason 也没有 [DONE]：客户端得到的
		// 唯一信号就是连接断开。
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		if conn, _, err := hijacker.Hijack(); err == nil {
			_ = conn.Close()
		}
	}))
	defer srv.Close()

	client := newTestClient(t, compatConfig(srv.URL))

	stream, err := client.Stream(context.Background(), ChatRequest{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var text strings.Builder
	deadline := time.Now().Add(5 * time.Second)
	for delta := range stream.Text() {
		text.WriteString(delta)
		if time.Now().After(deadline) {
			t.Fatal("stream.Text() never closed after the connection dropped")
		}
	}
	// The partial content is delivered, and the break surfaces as an error —
	// the framework does not silently pretend the generation finished.
	// 已收到的部分内容会交付，断连以错误浮出——框架不会假装生成已完成。
	if got := text.String(); got != "Hel" {
		t.Fatalf("text before the drop = %q, want %q", got, "Hel")
	}
	if stream.Err() == nil {
		t.Fatal("a mid-stream disconnect must surface an error, not a clean stop")
	}
}

func sseChunk(content string) string {
	payload, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"delta": map[string]any{"content": content}, "index": 0}},
	})
	return string(payload)
}
