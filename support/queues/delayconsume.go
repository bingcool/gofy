package queues

import (
	"context"
	"fmt"
	"sync"
)

// Consume handles due delayed messages until ctx is cancelled. It has the same
// signature as Queue.Consume, so the same worker adapter
// (worker/consumer/queuesconsumer) drives both.
//
// Pop already deletes the messages from the ZSET, so in-flight work is
// at-most-once: a crash between Pop and a successful handler loses that batch.
// A cancelled context is not a crash: the batch is written back before Consume
// returns, so a rolling restart does not discard what it had just popped.
// Retry writes a failed message back with a fresh due time, up to MaxRetries;
// an InFlightError writes it back after InFlightDelay without touching the
// retry budget. ConsumeOptions.PopLimit bounds one batch; IdleDelay is the pause
// when nothing is due, and can be short without hammering Redis because a poll
// is one Lua range query.
//
// Consume 持续处理到期消息直到 ctx 被取消。它与 Queue.Consume 签名相同，因此两者
// 可以共用同一个 worker 适配器（worker/consumer/queuesconsumer）。
//
// Pop 已经把消息从 ZSET 删除，在途消息因此是"至多一次"：Pop 到 handler 成功之间
// 进程崩溃会丢这一批。ctx 被取消不是崩溃：整批会在 Consume 返回前写回，一次滚动
// 重启不会丢掉刚弹出的消息。失败消息由 Retry 以新的到期时间写回，受 MaxRetries
// 限制；InFlightError 则在 InFlightDelay 之后写回且不消耗重试预算。
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
		for i, msg := range messages {
			if ctx.Err() != nil {
				// Pop already removed this batch from the ZSET, so everything
				// from here on exists only in this slice. Leaving now without a
				// write-back discards up to PopLimit messages.
				// Pop 已经把这一批从 ZSET 删除，从这条起的所有消息只存在于这个切片里。
				// 此时直接返回会丢掉最多 PopLimit 条消息。
				q.requeueAfterCancel(ctx, opts, messages[i:]...)
				return nil
			}
			if err := callHandler(ctx, handler, msg); err != nil {
				if ctx.Err() != nil {
					// Same reason, and the failed message is included: the
					// failure is the cancellation, not the message.
					// 同样的原因，失败的那条也在内：导致失败的是取消，不是消息本身。
					q.requeueAfterCancel(ctx, opts, messages[i:]...)
					return nil
				}
				if isInFlight(err) {
					// Another instance still owns the message. Re-queue it
					// without touching RetryNum; DiscardOnError must not apply
					// either.
					// 另一实例仍持有这条消息。重新入队但不改 RetryNum；
					// DiscardOnError 同样不适用。
					if err := q.RetryInFlight(ctx, msg, opts.InFlightDelay); err != nil {
						reportNack(opts, err)
					}
					continue
				}
				report(opts, err)
				if opts.DiscardOnError {
					continue
				}
				retried, retryErr := q.Retry(ctx, msg, opts.RetryDelay)
				if retryErr != nil {
					reportNack(opts, retryErr)
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

// requeueAfterCancel writes a popped batch back into the ZSET after the consume
// context was cancelled. The retry budget is left untouched: a handler that
// failed with a cancelled context says nothing about the message, and charging
// it could drop the last copy during a shutdown. Delay zero makes the messages
// due again at once, so the next poll anywhere picks them up.
// requeueAfterCancel 在消费上下文取消后把已弹出的整批写回 ZSET。它不改重试预算：
// 带着已取消的 context 失败不能说明消息有问题，扣预算可能在停机时丢掉最后一份副本。
// 延迟为零让消息立刻重新到期，任何实例的下一次轮询都能取到。
func (q *DelayQueue) requeueAfterCancel(ctx context.Context, opts ConsumeOptions, msgs ...*Message) {
	if len(msgs) == 0 {
		return
	}
	writeCtx, cancel := shutdownRequeue(ctx)
	defer cancel()
	for _, msg := range msgs {
		if err := q.RetryInFlight(writeCtx, msg, 0); err != nil {
			reportNack(opts, err)
		}
	}
}
