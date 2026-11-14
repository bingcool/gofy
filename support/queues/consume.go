package queues

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	// DefaultIdleDelay is how long Consume waits after an empty poll when
	// IdleDelay is not configured.
	// DefaultIdleDelay 是未配置 IdleDelay 时 Consume 空轮询后的等待时间。
	DefaultIdleDelay = 200 * time.Millisecond

	// DefaultRetryDelay is the delay before a failed message re-enters the main
	// queue when RetryDelay is not configured.
	// DefaultRetryDelay 是未配置 RetryDelay 时失败消息重新进入主队列前的延迟。
	DefaultRetryDelay = 3 * time.Second
)

var (
	// ErrHandlerRequired means Consume was called without a handler.
	// ErrHandlerRequired 表示调用 Consume 时没有提供处理函数。
	ErrHandlerRequired = errors.New("queues: consume handler is required")

	// ErrRetryExhausted reports a message that was dropped because it reached
	// MaxRetries. It is delivered to ConsumeOptions.OnError.
	// ErrRetryExhausted 表示消息达到 MaxRetries 后被丢弃，会传给
	// ConsumeOptions.OnError。
	ErrRetryExhausted = errors.New("queues: retry limit reached")
)

// HandlerFunc handles one message. A non-nil error schedules a retry unless
// ConsumeOptions.DiscardOnError is set; a panic is recovered and treated the
// same way as an error.
// HandlerFunc 处理一条消息。返回非 nil 时按重试处理，除非设置了
// ConsumeOptions.DiscardOnError；panic 会被捕获并按错误处理。
type HandlerFunc func(ctx context.Context, msg *Message) error

// ConsumeOptions controls one Consume call.
// ConsumeOptions 控制单次 Consume 调用。
type ConsumeOptions struct {
	// IdleDelay is the pause after an empty poll; zero means DefaultIdleDelay.
	// Consume polls with Pop rather than BRPOP because the blocking form only
	// has second granularity, which would delay both shutdown and the
	// promotion of due retries.
	// IdleDelay 是空轮询后的等待时间，0 表示 DefaultIdleDelay。
	// Consume 用非阻塞的 Pop 轮询而不是 BRPOP：阻塞形式只有秒级精度，会同时
	// 拖慢停机响应与到期重试的回流。
	IdleDelay time.Duration
	// RetryDelay is the delay before a failed message re-enters the main queue;
	// zero means DefaultRetryDelay.
	// RetryDelay 是失败消息重新进入主队列前的延迟，0 表示 DefaultRetryDelay。
	RetryDelay time.Duration
	// DiscardOnError drops a failed message instead of retrying it.
	// DiscardOnError 丢弃失败消息，不安排重试。
	DiscardOnError bool
	// PopLimit is how many due messages DelayQueue.Consume takes in one poll;
	// zero means 10. Queue.Consume pops one message at a time and ignores it.
	// PopLimit 是 DelayQueue.Consume 一次轮询取出的到期消息上限，0 表示 10。
	// Queue.Consume 每次只弹一条，忽略这个字段。
	PopLimit int
	// OnError receives handler failures, retry scheduling failures and dropped
	// messages. It is called from the Consume goroutine and must not block.
	// OnError 接收 handler 失败、重试安排失败与被丢弃的消息，在 Consume 的
	// goroutine 中调用，不得阻塞。
	OnError func(error)
}

func (o ConsumeOptions) withDefaults() ConsumeOptions {
	if o.IdleDelay <= 0 {
		o.IdleDelay = DefaultIdleDelay
	}
	if o.RetryDelay <= 0 {
		o.RetryDelay = DefaultRetryDelay
	}
	return o
}

// Consume handles messages until ctx is cancelled. It returns nil on
// cancellation, matching the MQ consumers, so it can be used directly as a
// goroutine body or as a worker.Worker.
//
// A popped message is already removed from Redis, so a process crash while a
// handler runs loses that message: this queue is at-most-once for in-flight
// work. Use an MQ consumer or a claimed task table (support/claim) when a lost
// in-flight message is unacceptable.
// Consume 持续处理消息直到 ctx 被取消。取消时返回 nil，与各 MQ 消费者一致，
// 因此可以直接作为 goroutine 主体或 worker.Worker 使用。
//
// 消息在 Pop 时已从 Redis 删除，handler 执行期间进程崩溃会丢这一条：本队列对
// 在途消息是"至多一次"。不能丢在途消息的场景请用 MQ 消费者或认领式任务表
// （support/claim）。
func (q *Queue) Consume(ctx context.Context, handler HandlerFunc, opts ConsumeOptions) error {
	if handler == nil {
		return ErrHandlerRequired
	}
	ctx = contextOrBackground(ctx)
	opts = opts.withDefaults()

	for {
		if ctx.Err() != nil {
			return nil
		}
		msg, ok, err := q.Pop(ctx, 0)
		if err != nil {
			// A cancelled context surfaces as a pop error; treat it as the
			// normal way out instead of reporting a fake failure.
			// ctx 取消会表现为弹出错误，这里按正常退出处理，不上报假失败。
			if ctx.Err() != nil {
				return nil
			}
			report(opts, err)
			if !wait(ctx, opts.RetryDelay) {
				return nil
			}
			continue
		}
		if !ok {
			if !wait(ctx, opts.IdleDelay) {
				return nil
			}
			continue
		}
		if err := callHandler(ctx, handler, msg); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			report(opts, err)
			if opts.DiscardOnError {
				continue
			}
			retried, retryErr := q.Retry(ctx, msg, opts.RetryDelay)
			if retryErr != nil {
				report(opts, retryErr)
				continue
			}
			if !retried {
				report(opts, fmt.Errorf("%w: message %s after %d retries",
					ErrRetryExhausted, msg.ID, q.maxRetries))
			}
		}
	}
}

// callHandler runs the handler with panic recovery so one bad message cannot
// take down the consumer.
// callHandler 执行 handler 并捕获 panic，避免一条坏消息拖垮整个消费者。
func callHandler(ctx context.Context, handler HandlerFunc, msg *Message) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("queues: handler panic: %v", rec)
		}
	}()
	return handler(ctx, msg)
}

func report(opts ConsumeOptions, err error) {
	if opts.OnError != nil {
		opts.OnError(err)
	}
}

// wait pauses for d and reports whether the context is still alive.
// wait 等待 d，并返回 context 是否仍然有效。
func wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
