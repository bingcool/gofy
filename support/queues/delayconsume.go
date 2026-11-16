package queues

import (
	"context"
	"fmt"
	"sync"
)

// Consume handles due delayed messages until ctx is cancelled. It has the same
// signature as Queue.Consume, so the same worker adapter
// (support/worker/consumer/queuesconsumer) drives both.
//
// Pop already deletes the messages from the ZSET, so in-flight work is
// at-most-once: a crash between Pop and a successful handler loses that batch.
// Retry writes a failed message back with a fresh due time, up to MaxRetries.
// ConsumeOptions.PopLimit bounds one batch; IdleDelay is the pause when nothing
// is due, and can be short without hammering Redis because a poll is one Lua
// range query.
//
// Consume 持续处理到期消息直到 ctx 被取消。它与 Queue.Consume 签名相同，因此两者
// 可以共用同一个 worker 适配器（support/worker/consumer/queuesconsumer）。
//
// Pop 已经把消息从 ZSET 删除，在途消息因此是"至多一次"：Pop 到 handler 成功之间
// 进程崩溃会丢这一批。失败消息由 Retry 以新的到期时间写回，受 MaxRetries 限制。
// ConsumeOptions.PopLimit 限制单批条数；没有到期消息时按 IdleDelay 暂停，一次轮询
// 只是一次 Lua range 查询，间隔可以设得较短。
func (q *DelayQueue) Consume(ctx context.Context, handler HandlerFunc, opts ConsumeOptions) error {
	if handler == nil {
		return ErrHandlerRequired
	}
	ctx = contextOrBackground(ctx)
	opts = opts.withDefaults()
	limit := opts.PopLimit
	if limit <= 0 {
		limit = defaultPopLimit
	}

	var ready sync.Once
	reportReady := func() {
		if opts.OnReady == nil {
			return
		}
		ready.Do(opts.OnReady)
	}

	for {
		if ctx.Err() != nil {
			return nil
		}
		messages, err := q.Pop(ctx, limit)
		if err != nil {
			// A cancelled context surfaces as a Redis error; treat it as the
			// normal way out instead of reporting a fake failure.
			// ctx 取消会以 Redis 错误的形式冒出来，按正常退出处理，不报假失败。
			if ctx.Err() != nil {
				return nil
			}
			report(opts, err)
			if !wait(ctx, opts.RetryDelay) {
				return nil
			}
			continue
		}
		// Same rule as Queue.Consume: one successful range query is the whole
		// readiness story of a polled queue, empty result included.
		// 与 Queue.Consume 同一规则：一次成功的范围查询就是轮询队列的全部就绪依据，空结果
		// 也算。
		reportReady()
		if len(messages) == 0 {
			if !wait(ctx, opts.IdleDelay) {
				return nil
			}
			continue
		}
		for _, msg := range messages {
			if ctx.Err() != nil {
				return nil
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
					report(opts, fmt.Errorf("%w: delayed message %s after %d retries",
						ErrRetryExhausted, msg.ID, q.maxRetries))
				}
			}
		}
	}
}
