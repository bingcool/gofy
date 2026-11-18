package ai

import (
	"context"
	"sync"
	"time"

	goai "github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
)

// Stream is a streaming generation. Exactly one of Text and Chunks may be
// consumed — goai's TextStream is a single reader — and Result may always be
// called afterwards for the accumulated answer and token usage.
//
// A stream that is neither consumed nor closed keeps a goroutine alive, so
// Close is required when the caller stops early.
// Stream 是一次流式生成。Text 与 Chunks 只能消费其中一个——goai 的 TextStream
// 是单读者——Result 之后随时可以取累计答案与 token 用量。
//
// 既不消费也不关闭的流会留下一个 goroutine，因此调用方提前中止时必须 Close。
type Stream struct {
	alias   string
	inner   *goai.TextStream
	cancel  context.CancelFunc
	client  *Client
	started time.Time

	once   sync.Once
	result *ChatResponse
}

// Stream starts a streaming generation. The returned Stream owns a derived
// context; Close cancels it, which is how an abandoned stream releases its
// connection.
// Stream 启动一次流式生成。返回的 Stream 拥有一个派生 context；Close 取消它，
// 被放弃的流因此能释放连接。
func (c *Client) Stream(ctx context.Context, req ChatRequest) (*Stream, error) {
	p, err := c.prepare(req)
	if err != nil {
		return nil, err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	started := time.Now()
	inner, err := goai.StreamText(streamCtx, p.model, p.opts...)
	if err != nil {
		cancel()
		return nil, c.fail(p.alias, time.Since(started), err)
	}
	return &Stream{
		alias:   p.alias,
		inner:   inner,
		cancel:  cancel,
		client:  c,
		started: started,
	}, nil
}

// Text returns the text deltas. Mutually exclusive with Chunks.
// Text 返回文本增量，与 Chunks 互斥。
func (s *Stream) Text() <-chan string {
	if s == nil || s.inner == nil {
		closed := make(chan string)
		close(closed)
		return closed
	}
	return s.inner.TextStream()
}

// Chunks returns raw provider chunks, including tool calls and finish events.
// Mutually exclusive with Text.
// Chunks 返回 provider 原始分块（含工具调用与结束事件），与 Text 互斥。
func (s *Stream) Chunks() <-chan provider.StreamChunk {
	if s == nil || s.inner == nil {
		closed := make(chan provider.StreamChunk)
		close(closed)
		return closed
	}
	return s.inner.Stream()
}

// Result returns the accumulated response. It blocks until the stream is done,
// which is why it is safe to call right after ranging over Text. Usage is
// reported through WithOnUsage exactly once, on the first call.
// Result 返回累计结果。它会阻塞到流结束，因此在 range Text 之后立刻调用是安全的。
// 用量在第一次调用时经 WithOnUsage 上报，且只上报一次。
func (s *Stream) Result() *ChatResponse {
	if s == nil || s.inner == nil || s.client == nil {
		return nil
	}
	s.once.Do(func() {
		res := s.inner.Result()
		if res == nil {
			return
		}
		elapsed := time.Since(s.started)
		s.result = &ChatResponse{
			Text:         res.Text,
			Reasoning:    res.Reasoning,
			ToolCalls:    res.ToolCalls,
			FinishReason: res.FinishReason,
			Usage:        usageFrom(res.TotalUsage),
			ModelAlias:   s.alias,
			Elapsed:      elapsed,
		}
		s.client.report(UsageEvent{
			Alias:    s.alias,
			Provider: s.client.meta[s.alias].Provider,
			Model:    s.client.meta[s.alias].Model,
			Usage:    s.result.Usage,
			Elapsed:  elapsed,
		})
		s.client.logCall(s.alias, s.result.Usage, elapsed, res.FinishReason, res.Text, ChatRequest{})
	})
	return s.result
}

// Err returns the stream's terminal error, or nil.
// Err 返回流的终止错误，没有则为 nil。
func (s *Stream) Err() error {
	if s == nil || s.inner == nil || s.client == nil {
		return nil
	}
	if err := s.inner.Err(); err != nil {
		return s.client.errFor(s.alias, err)
	}
	return nil
}

// Close cancels the stream. It is idempotent and safe to call before or after
// consuming. Result and Err stay readable afterwards for whatever arrived.
// Close 取消流。可重复调用，在消费前后都可以调；之后仍可读 Result 与 Err，
// 拿到已经到达的部分。
func (s *Stream) Close() error {
	if s == nil || s.cancel == nil {
		return nil
	}
	s.cancel()
	return nil
}
