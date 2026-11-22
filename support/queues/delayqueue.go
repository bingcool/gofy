package queues

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// defaultPopLimit is the maximum number of messages one DelayQueue.Pop takes when
// no limit is given.
// defaultPopLimit 是 DelayQueue.Pop 未指定 limit 时一次取出的最大条数。
const defaultPopLimit = 10

// maxPopLimit caps one Pop regardless of what the caller asks for. The Lua
// script deletes the batch with unpack(members), and Redis' Lua interpreter
// refuses to unpack more than about 8000 values, which would turn a large batch
// into a script error for the whole poll. It also bounds the handler work one
// round can create, since a popped batch lives only in the caller's slice.
// maxPopLimit 是 Pop 无论调用方要求多少都不超过的上限。Lua 脚本用
// unpack(members) 删除整批，而 Redis 的 Lua 解释器最多只能 unpack 约 8000 个值，
// 超过会让整次轮询变成脚本错误；它同时限制了一轮能产生的 handler 工作量，因为弹出的
// 一批消息只存在于调用方的切片里。
const maxPopLimit = 1000

// DelayQueue is a delayed queue backed by a Redis ZSET: the due timestamp is the
// score and Pop takes and deletes due messages atomically through a Lua script.
// DelayQueue 是基于 Redis ZSET 的延迟队列：消息以到期时间戳作为 score，
// Pop 通过 Lua 脚本原子地取出并删除已到期消息。
type DelayQueue struct {
	client     goredis.Cmdable
	key        string
	maxRetries int
}

// NewDelayQueue builds a delayed queue. A nil client returns ErrClientRequired.
// NewDelayQueue 构造延迟队列。client 为 nil 时返回 ErrClientRequired。
func NewDelayQueue(client goredis.Cmdable, cfg Config) (*DelayQueue, error) {
	if client == nil {
		return nil, ErrClientRequired
	}
	cfg, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	return &DelayQueue{
		client:     client,
		key:        cfg.Key,
		maxRetries: cfg.MaxRetries,
	}, nil
}

// Key returns the ZSET key of the delayed queue.
// Key 返回延迟队列的 ZSET 键。
func (q *DelayQueue) Key() string {
	return q.key
}

// MaxRetries returns the maximum number of retries allowed after a failure.
// MaxRetries 返回消息失败后允许的最大重试次数。
func (q *DelayQueue) MaxRetries() int {
	return q.maxRetries
}

// Add writes a delayed message that Pop can only take after delay.
// Add 写入一条延迟消息，delay 之后才能被 Pop 取出。
func (q *DelayQueue) Add(ctx context.Context, payload any, delay time.Duration) error {
	msg, err := newMessage(payload)
	if err != nil {
		return err
	}
	raw, err := encodeMessage(msg)
	if err != nil {
		return err
	}
	at := time.Now().Add(normalizeDelay(delay))
	member := goredis.Z{Score: score(at), Member: raw}
	if err := q.client.ZAdd(contextOrBackground(ctx), q.key, member).Err(); err != nil {
		return fmt.Errorf("queues: add delayed message %q: %w", q.key, err)
	}
	return nil
}

// Pop takes and deletes up to limit due messages, returned in ascending due-time
// order. limit <= 0 uses the default of 10, and anything above maxPopLimit is
// clamped to it; an empty slice means nothing is due.
// Pop 取出并删除至多 limit 条已到期的消息，按到期时间升序返回。
// limit <= 0 时使用默认值 10，超过 maxPopLimit 时按上限截断；没有到期消息时返回空切片。
func (q *DelayQueue) Pop(ctx context.Context, limit int) ([]*Message, error) {
	if limit <= 0 {
		limit = defaultPopLimit
	}
	if limit > maxPopLimit {
		limit = maxPopLimit
	}
	raw, err := popDueScript.Run(contextOrBackground(ctx), q.client, []string{q.key}, score(time.Now()), limit).StringSlice()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queues: pop delayed messages %q: %w", q.key, err)
	}
	messages := make([]*Message, 0, len(raw))
	for _, item := range raw {
		msg, err := decodeMessage(item)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

// Retry writes a failed message back into the delayed ZSET with a new due time.
// A false result means the retry limit was reached and the message is dropped.
// Retry 把消费失败的消息以新的到期时间重新写回延迟 ZSET。
// 返回 false 表示已达到重试上限，消息被丢弃。
func (q *DelayQueue) Retry(ctx context.Context, msg *Message, delay time.Duration) (bool, error) {
	return scheduleRetry(ctx, q.client, q.key, q.maxRetries, msg, delay)
}

// RetryInFlight writes a message back into the delayed ZSET without changing
// RetryNum or applying MaxRetries: the delivery is not a failure yet. Use it
// when another instance still holds the processing lease and this copy must not
// be dropped, and make delay cover that lease.
// RetryInFlight 把消息重新写回延迟 ZSET，不改 RetryNum，也不受 MaxRetries 限制：
// 这次投递尚未失败。它用于另一实例仍持有处理租约、这份副本不能丢的场景，
// delay 应覆盖该租约。
func (q *DelayQueue) RetryInFlight(ctx context.Context, msg *Message, delay time.Duration) error {
	return scheduleDeferred(ctx, q.client, q.key, msg, delay)
}

// Len returns the total number of messages in the delayed queue, including those
// that are not due yet.
// Len 返回延迟队列中的消息总数（包含尚未到期的消息）。
func (q *DelayQueue) Len(ctx context.Context) (int64, error) {
	length, err := q.client.ZCard(contextOrBackground(ctx), q.key).Result()
	if err != nil {
		return 0, fmt.Errorf("queues: len %q: %w", q.key, err)
	}
	return length, nil
}
