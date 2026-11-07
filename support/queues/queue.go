// Package queues 在注入的 Redis 客户端之上提供即时队列与延迟队列。
//
// 键布局与 swoolefy 的 Queues 组件保持一致：
//
//	{key}                    主队列（List）
//	{key}:priority_queue     优先队列（List）
//	{key}:retry_queue_sort   重试队列（ZSET）
//
// 与 support/cache、support/redis 一样，本包不持有全局状态，也不接管客户端生命周期。
package queues

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	// DefaultMaxRetries 是未配置 MaxRetries 时一条消息失败后允许的重试次数。
	DefaultMaxRetries = 3

	// retryPromoteLimit 控制每次 Pop 最多从重试 ZSET 回流多少条消息。
	retryPromoteLimit = 100
)

var (
	// ErrClientRequired 表示构造队列时没有注入 Redis 客户端。
	ErrClientRequired = errors.New("queues: redis client is required")

	// ErrKeyRequired 表示构造队列时没有提供队列名。
	ErrKeyRequired = errors.New("queues: queue key is required")
)

// Config 描述一个队列实例。即时队列和延迟队列共用该配置，
// 其中 Key 对延迟队列来说就是 ZSET 的键。
type Config struct {
	// Key 队列名，同时作为 Redis 键前缀。
	Key string
	// MaxRetries 消息失败后最多重新投递的次数，0 表示使用 DefaultMaxRetries。
	MaxRetries int
}

func (c Config) validate() (Config, error) {
	c.Key = strings.TrimSpace(c.Key)
	if c.Key == "" {
		return Config{}, ErrKeyRequired
	}
	if c.MaxRetries < 0 {
		return Config{}, fmt.Errorf("queues: max retries must not be negative")
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = DefaultMaxRetries
	}
	return c, nil
}

// Queue 是基于 Redis List 的即时队列：LPUSH 入队、RPOP 出队，
// 优先队列会被优先消费，失败消息进入重试 ZSET 等待回流。
type Queue struct {
	client      goredis.Cmdable
	key         string
	priorityKey string
	retryKey    string
	maxRetries  int
}

// NewQueue 构造即时队列。client 为 nil 时返回 ErrClientRequired。
func NewQueue(client goredis.Cmdable, cfg Config) (*Queue, error) {
	if client == nil {
		return nil, ErrClientRequired
	}
	cfg, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	return &Queue{
		client:      client,
		key:         cfg.Key,
		priorityKey: cfg.Key + ":priority_queue",
		retryKey:    cfg.Key + ":retry_queue_sort",
		maxRetries:  cfg.MaxRetries,
	}, nil
}

// Key 返回主队列键。
func (q *Queue) Key() string {
	return q.key
}

// PriorityKey 返回优先队列键。
func (q *Queue) PriorityKey() string {
	return q.priorityKey
}

// RetryKey 返回重试 ZSET 键。
func (q *Queue) RetryKey() string {
	return q.retryKey
}

// MaxRetries 返回消息失败后允许的最大重试次数。
func (q *Queue) MaxRetries() int {
	return q.maxRetries
}

// Push 把消息追加到主队列，返回入队条数。
func (q *Queue) Push(ctx context.Context, payloads ...any) (int, error) {
	return q.push(ctx, q.key, payloads)
}

// PushPriority 把消息写入优先队列，Pop 时会先消费优先队列。
func (q *Queue) PushPriority(ctx context.Context, payloads ...any) (int, error) {
	return q.push(ctx, q.priorityKey, payloads)
}

func (q *Queue) push(ctx context.Context, key string, payloads []any) (int, error) {
	if len(payloads) == 0 {
		return 0, nil
	}
	encoded := make([]string, 0, len(payloads))
	for _, payload := range payloads {
		msg, err := newMessage(payload)
		if err != nil {
			return 0, err
		}
		raw, err := encodeMessage(msg)
		if err != nil {
			return 0, err
		}
		encoded = append(encoded, raw)
	}
	if err := q.client.LPush(contextOrBackground(ctx), key, toAny(encoded)...).Err(); err != nil {
		return 0, fmt.Errorf("queues: push %q: %w", key, err)
	}
	return len(encoded), nil
}

// Pop 取出下一条消息。timeout <= 0 时立即返回，没有消息时 ok=false；
// timeout > 0 时阻塞等待，阻塞精度为秒，非整秒会向上取整。
// 每次弹出前会先把重试 ZSET 中已到期的消息回流到主队列。
func (q *Queue) Pop(ctx context.Context, timeout time.Duration) (*Message, bool, error) {
	ctx = contextOrBackground(ctx)
	if err := q.promoteRetries(ctx); err != nil {
		return nil, false, err
	}

	keys := []string{q.priorityKey, q.key}
	if timeout <= 0 {
		for _, key := range keys {
			raw, err := q.client.RPop(ctx, key).Result()
			if errors.Is(err, goredis.Nil) {
				continue
			}
			if err != nil {
				return nil, false, fmt.Errorf("queues: pop %q: %w", key, err)
			}
			return decodePopped(raw)
		}
		return nil, false, nil
	}

	// Redis 的阻塞超时以秒为单位，向上取整避免 go-redis 向下取整后提前返回。
	blockFor := timeout
	if remainder := timeout % time.Second; remainder != 0 {
		blockFor += time.Second - remainder
	}
	values, err := q.client.BRPop(ctx, blockFor, keys...).Result()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("queues: pop: %w", err)
	}
	if len(values) < 2 {
		return nil, false, nil
	}
	return decodePopped(values[1])
}

// Retry 把消费失败的消息放回重试 ZSET，delay 之后重新进入主队列。
// 返回 false 表示已达到重试上限，消息被丢弃。
func (q *Queue) Retry(ctx context.Context, msg *Message, delay time.Duration) (bool, error) {
	return scheduleRetry(ctx, q.client, q.retryKey, q.maxRetries, msg, delay)
}

// Len 返回主队列当前长度（不包含优先队列和重试队列）。
func (q *Queue) Len(ctx context.Context) (int64, error) {
	length, err := q.client.LLen(contextOrBackground(ctx), q.key).Result()
	if err != nil {
		return 0, fmt.Errorf("queues: len %q: %w", q.key, err)
	}
	return length, nil
}

// promoteRetries 把到期的重试消息原子回流到主队列。
func (q *Queue) promoteRetries(ctx context.Context) error {
	keys := []string{q.retryKey, q.key}
	if err := promoteRetryScript.Run(ctx, q.client, keys, score(time.Now()), retryPromoteLimit).Err(); err != nil {
		return fmt.Errorf("queues: promote retries %q: %w", q.retryKey, err)
	}
	return nil
}

func decodePopped(raw string) (*Message, bool, error) {
	msg, err := decodeMessage(raw)
	if err != nil {
		return nil, false, err
	}
	return msg, true, nil
}

func toAny(values []string) []any {
	items := make([]any, len(values))
	for i, value := range values {
		items[i] = value
	}
	return items
}
