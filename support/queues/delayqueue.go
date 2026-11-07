package queues

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// defaultPopLimit 是 DelayQueue.Pop 未指定 limit 时一次取出的最大条数。
const defaultPopLimit = 10

// DelayQueue 是基于 Redis ZSET 的延迟队列：消息以到期时间戳作为 score，
// Pop 通过 Lua 脚本原子地取出并删除已到期消息。
type DelayQueue struct {
	client     goredis.Cmdable
	key        string
	maxRetries int
}

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

// Key 返回延迟队列的 ZSET 键。
func (q *DelayQueue) Key() string {
	return q.key
}

// MaxRetries 返回消息失败后允许的最大重试次数。
func (q *DelayQueue) MaxRetries() int {
	return q.maxRetries
}

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

// Pop 取出并删除至多 limit 条已到期的消息，按到期时间升序返回。
// limit <= 0 时使用默认值 10；没有到期消息时返回空切片。
func (q *DelayQueue) Pop(ctx context.Context, limit int) ([]*Message, error) {
	if limit <= 0 {
		limit = defaultPopLimit
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

// Retry 把消费失败的消息以新的到期时间重新写回延迟 ZSET。
// 返回 false 表示已达到重试上限，消息被丢弃。
func (q *DelayQueue) Retry(ctx context.Context, msg *Message, delay time.Duration) (bool, error) {
	return scheduleRetry(ctx, q.client, q.key, q.maxRetries, msg, delay)
}

// Len 返回延迟队列中的消息总数（包含尚未到期的消息）。
func (q *DelayQueue) Len(ctx context.Context) (int64, error) {
	length, err := q.client.ZCard(contextOrBackground(ctx), q.key).Result()
	if err != nil {
		return 0, fmt.Errorf("queues: len %q: %w", q.key, err)
	}
	return length, nil
}
