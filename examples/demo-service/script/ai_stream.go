package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bingcool/gofy/apperr"
	gofyscript "github.com/bingcool/gofy/script"
	supportai "github.com/bingcool/gofy/support/ai"
	"github.com/spf13/pflag"
	"github.com/yourorg/demo-service/app/config"
	"go.uber.org/zap"
)

// Defaults for the streaming sample. They are plain values rather than a config
// section on purpose: this is a demonstration, and a demonstration should run
// with no configuration beyond llm_ai.
// 流式样例的默认值。这里用普通常量而不是配置项：它是演示，除 llm_ai 之外不该再要求
// 任何配置就能跑。
const (
	aiStreamDefaultPrompt    = "用三句话说明流式输出适合什么场景。"
	aiStreamDefaultMaxTokens = 256
	aiStreamDefaultTimeout   = 60 * time.Second
)

// AIStream streams one completion, to stdout or as Server-Sent Events.
//
// It is the runnable counterpart of the snippet in support/ai/README.md and
// answers the four questions that snippet leaves open:
//
//  1. how deltas are consumed (range over Stream.Text, print, done);
//  2. how a caller stops early without leaking the consume goroutine;
//  3. which errors are failures and which are just "we cancelled it";
//  4. how the same stream becomes an SSE response for a browser or curl.
//
// It declares WithoutDatabase: the whole run touches only the LLM endpoint, so
// opening MySQL, Redis, Kafka and the rest would be cost with no purpose.
// AIStream 流式输出一次生成，输出到 stdout 或作为 SSE 提供。
//
// 它是 support/ai/README.md 里那段片段的"可运行版"，补上片段没说清的四件事：
//
//  1. 增量怎么消费（range Stream.Text，打印，结束）；
//  2. 提前中止怎么停，才不会漏掉消费 goroutine；
//  3. 哪些错误算失败、哪些只是"我们自己取消的"；
//  4. 同一条流怎么变成浏览器或 curl 能用的 SSE 响应。
//
// 它声明了 WithoutDatabase：整个过程只访问 LLM 端点，打开 MySQL、Redis、Kafka
// 只会是纯粹的成本。
func AIStream(cfg *config.Config) gofyscript.Script {
	return gofyscript.Script{
		Name:            "ai-stream",
		Short:           "stream one completion to stdout, or serve it as SSE",
		WithoutDatabase: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("prompt", aiStreamDefaultPrompt, "prompt text")
			fs.String("model", "", "model alias (default: llm_ai.default_model)")
			fs.Int("max-tokens", aiStreamDefaultMaxTokens, "cap on the billed completion")
			fs.Duration("timeout", aiStreamDefaultTimeout, "deadline for the whole stream, not for the first byte")
			fs.Int("stop-after", 0, "stop after N deltas to demonstrate early termination (0 = read to the end)")
			fs.String("serve", "", "serve SSE on this address instead of printing, e.g. 127.0.0.1:8099")
		},
		Run: func(ctx context.Context, rt *gofyscript.Runtime, fs *pflag.FlagSet) error {
			llmCfg := cfg.LLMAI
			if !llmCfg.Enabled {
				return apperr.BadRequest("llm_ai is disabled in config")
			}
			// The client lives for one run. A script has no reload path and no
			// long-lived pool to keep: Close releases the HTTP connections when
			// the run ends, which is also what stops the flow-control goroutine
			// behind the stream.
			// 客户端只活一次运行。脚本没有 Reload 路径，也没有需要长期保留的连接池：
			// Run 结束时的 Close 释放 HTTP 连接，也顺带停掉流背后的流控 goroutine。
			client, err := supportai.New(llmCfg, supportai.WithLogger(rt.Logger))
			if err != nil {
				return apperr.Internal("build llm client").WithCause(err)
			}
			defer func() { _ = client.Close() }()

			prompt, _ := fs.GetString("prompt")
			alias, _ := fs.GetString("model")
			maxTokens, _ := fs.GetInt("max-tokens")
			timeout, _ := fs.GetDuration("timeout")
			stopAfter, _ := fs.GetInt("stop-after")
			serveAddr, _ := fs.GetString("serve")

			// The deadline is applied per stream, not around the server: in SSE
			// mode the process serves until it is signalled, and one long-lived
			// request must not become the server's lifetime.
			// deadline 按"每条流"计，不套在服务器外面：SSE 模式下进程一直服务到收到信号，
			// 单个长请求不该变成服务器的生命周期。
			req := supportai.ChatRequest{
				ModelAlias:      alias,
				Prompt:          prompt,
				MaxOutputTokens: maxTokens,
			}
			if serveAddr != "" {
				return serveSSE(ctx, rt.Logger, client, serveAddr, req, timeout)
			}
			return printStream(ctx, rt.Logger, client, req, stopAfter, timeout)
		},
	}
}

// printStream writes each delta as it arrives, then the usage the deltas cannot
// carry.
// printStream 边收边把增量写出去，最后补上增量本身携带不了的用量。
func printStream(ctx context.Context, lg *zap.Logger, client *supportai.Client, req supportai.ChatRequest, stopAfter int, timeout time.Duration) error {
	// The deadline covers the entire stream. A per-request timeout would only
	// bound the first byte, and a model emitting one token per second would then
	// run forever without ever tripping it.
	// deadline 覆盖整条流。只限制单次请求的话，约束的是首字节，一个每秒吐一个 token
	// 的模型可以永远不触发它。
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stream, err := client.Stream(ctx, req)
	if err != nil {
		return apperr.Internal("start stream").WithCause(err)
	}
	// Close is required in two situations and harmless in the third: an early
	// return (cancels the stream), an early break (unblocks Result/Err, see
	// below), and a fully drained stream (the consume goroutine is already gone).
	// It is idempotent, so deferring it and also calling it explicitly is fine.
	// Close 在三种情形下都要：提前返回（取消流）、提前 break（解开 Result/Err 的
	// 阻塞，见下）、以及正常读完后（消费 goroutine 已退出，调用无副作用）。它可重复
	// 调用，所以 defer 之外再显式调一次是安全的。
	defer func() { _ = stream.Close() }()

	deltas := 0
	stopped := false
	for delta := range stream.Text() {
		fmt.Print(delta)
		deltas++
		if stopAfter > 0 && deltas >= stopAfter {
			stopped = true
			break
		}
	}
	fmt.Println()

	if stopped {
		// Cancel before reading Result. The consume goroutine sends into a
		// 64-slot channel and blocks once nobody reads; Result waits for that
		// goroutine, so without this line it would wait forever.
		// 读 Result 之前必须先取消：消费 goroutine 往 64 容量的通道里发送，没人再读时
		// 它会阻塞，而 Result 要等它结束——少了这一行就是死等。
		_ = stream.Close()
	}

	// Err reports the terminal error, including a cancellation. A stop we asked
	// for is not a failure, and it is decided by *who* cancelled rather than by
	// what the provider reports: a cancelled HTTP read surfaces as whatever the
	// transport happened to return (context.Canceled, a read error, a provider
	// ChunkError), and classifying that string is a losing game. If we stopped
	// it, the partial answer on screen is still valid.
	// Err 返回流的终止错误，包含取消。自己要求的停止不是失败，而判断依据是"谁取消的"，
	// 不是 provider 报了什么：被取消的 HTTP 读取可能表现成 context.Canceled、读错误、
	// 或 provider 的 ChunkError，去分辨这些是徒劳的。只要我们主动停了，屏幕上那段部分
	// 文本就依然有效。
	if streamErr := stream.Err(); streamErr != nil {
		switch {
		case stopped:
			lg.Warn("stream stopped after requested deltas",
				zap.Int("deltas", deltas),
				zap.Error(streamErr),
			)
		case errors.Is(ctx.Err(), context.Canceled):
			lg.Warn("stream cancelled by caller", zap.Int("deltas", deltas))
		default:
			// Nothing cancelled it, so this is a real failure — including the
			// deadline, which is ours but still means the answer is incomplete.
			// 没有任何取消，这就是真正的失败——包括超时：deadline 是我们设的，但它仍
			// 意味着回答不完整。
			return apperr.Internal("stream").WithCause(streamErr)
		}
	}

	res := stream.Result()
	if res == nil {
		return apperr.Internal("stream returned no result")
	}
	// Usage only exists here: stream chunks carry text, not token counts, so a
	// caller that bills or logs must take it from the accumulated result. An
	// interrupted stream reports zero, because the provider only accounts for the
	// request when it finishes.
	// 用量只有这里有：流式分块里是文本，没有 token 计数，需要计费或记日志的调用方
	// 只能从累计结果里取。被中断的流这里是 0——provider 只在请求结束时结算。
	lg.Info("ai stream finished",
		zap.String("alias", res.ModelAlias),
		zap.Int("deltas", deltas),
		zap.Int("input_tokens", res.Usage.InputTokens),
		zap.Int("output_tokens", res.Usage.OutputTokens),
		zap.Bool("stopped_early", stopped),
		zap.Duration("elapsed", res.Elapsed),
	)
	// finish 按需拼接：流式响应并不总是带 finish_reason（DeepSeek 的两条端点实测都
	// 返回空串），空值打成 "finish=" 只会让人以为解析错了。
	finish := ""
	if res.FinishReason != "" {
		finish = " finish=" + string(res.FinishReason)
	}
	fmt.Printf("\n[alias=%s%s in=%d out=%d elapsed=%s]\n",
		res.ModelAlias, finish, res.Usage.InputTokens, res.Usage.OutputTokens,
		res.Elapsed.Round(time.Millisecond))
	return nil
}

// serveSSE exposes the same stream over HTTP. It exists to show the mapping
// every streaming endpoint needs: one delta becomes one event, the terminal
// usage becomes a final event, and the client's disconnect is what closes the
// stream.
// serveSSE 把同一条流通过 HTTP 提供出去，演示每个流式端点都要做的映射：一个增量
// 一个事件、终止用量作为最后一个事件、客户端断开就是关闭流的信号。
func serveSSE(ctx context.Context, lg *zap.Logger, client *supportai.Client, addr string, req supportai.ChatRequest, timeout time.Duration) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming is not supported by this connection", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// Send the headers before the first delta: without this flush the client
		// sees no response at all until the model produces its first token, and
		// a slow model looks like a hung endpoint.
		// 首个增量之前先把响应头发出去：不刷这一下，客户端在模型吐出第一个 token 前
		// 收不到任何响应，慢模型看起来就是端点挂了。
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// r.Context() is the disconnect signal. When the browser closes the tab
		// or curl is killed, it is cancelled, Close unblocks the consume
		// goroutine, and the deferred client.Close in Run still runs.
		// r.Context() 就是断开信号。浏览器关标签页或 curl 被杀时它被取消，Close 解开
		// 消费 goroutine 的阻塞，Run 里的 client.Close 也照常执行。
		streamCtx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		stream, err := client.Stream(streamCtx, req)
		if err != nil {
			writeSSE(w, flusher, "error", map[string]string{"error": err.Error()})
			return
		}
		defer func() { _ = stream.Close() }()

		for delta := range stream.Text() {
			writeSSE(w, flusher, "delta", map[string]string{"text": delta})
		}
		if err := stream.Err(); err != nil {
			// Same rule as printStream, one level down: a dead request context
			// means the caller is gone, and there is nobody left to read an error
			// event. Only a failure with a live client is worth reporting.
			// 与 printStream 同一条规则，只是低一层：请求上下文已经结束说明调用方走了，
			// 没有人在读错误事件；只有客户端还在时，失败才值得上报。
			if r.Context().Err() == nil {
				writeSSE(w, flusher, "error", map[string]string{"error": err.Error()})
			}
			return
		}

		res := stream.Result()
		if res == nil {
			return
		}
		done := map[string]any{
			"alias":      res.ModelAlias,
			"input":      res.Usage.InputTokens,
			"output":     res.Usage.OutputTokens,
			"elapsed_ms": res.Elapsed.Milliseconds(),
		}
		if res.FinishReason != "" {
			done["finish"] = string(res.FinishReason)
		}
		writeSSE(w, flusher, "done", done)
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	// Shutdown, not Close: in-flight streams get a moment to finish instead of
	// having their TCP connection cut mid-event.
	// 用 Shutdown 而不是 Close：在途的流有几秒收尾时间，而不是事件写到一半被切断连接。
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	lg.Info("sse listening", zap.String("addr", addr), zap.String("path", "/chat"))
	fmt.Printf("SSE ready: curl -N http://%s/chat\n", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return apperr.Internal("serve sse").WithCause(err)
	}
	return nil
}

// writeSSE writes one event. The payload is JSON-encoded rather than pasted in
// raw: SSE frames are newline-delimited, so a delta containing "\n\n" would end
// the event early and split one answer into several broken frames. json.Marshal
// keeps every payload on a single line.
// writeSSE 写出一个事件。payload 走 JSON 编码而不是原样拼接：SSE 帧以换行分隔，
// 一个含空行的增量会提前结束事件，把一段回答拆成几个坏帧；json.Marshal 能保证
// payload 始终在一行里。
func writeSSE(w http.ResponseWriter, f http.Flusher, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	f.Flush()
}
