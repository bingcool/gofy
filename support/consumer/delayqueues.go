package consumer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/bingcool/gofy/support/queues"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/bingcool/gofy/support/worker/consumer/queuesconsumer"
)

// DelayQueuesConfig describes one Redis delayed-queue subscription.
//
// Unlike QueuesConfig, messages are not available until their due time: the
// producer calls DelayQueue.Add(ctx, payload, delay) and the consumer polls the
// ZSET for due entries. Pop already removes the entry, so in-flight work is
// at-most-once — the same tradeoff as the immediate Redis queue.
// DelayQueuesConfig 描述一个 Redis 延迟队列订阅。
//
// 与 QueuesConfig 不同，消息要到到期时间才可消费：生产端调
// DelayQueue.Add(ctx, payload, delay)，消费端轮询 ZSET 取到期项。Pop 已把条目删除，
// 在途消息因此是"至多一次"——与立即 Redis 队列的取舍相同。
type DelayQueuesConfig struct {
	// Name is the worker name and the idempotency scope. Required.
	// Name 是 worker 名与幂等作用域。必填。
	Name string
	// Concurrency is the number of pollers. Pop is a Lua take-and-delete, so
	// several pollers never hand out the same due message twice.
	// Concurrency 是轮询实例数。Pop 是 Lua 原子「取出并删除」，多个实例不会重复
	// 拿到同一条到期消息。
	Concurrency int
	// Idempotency turns a redelivery into a no-op. Needs a database.
	// Idempotency 把重复投递变成空操作。需要数据库。
	Idempotency bool
	// MaxAttempts bounds how often one message is handled before it is parked
	// as dead. Zero means five.
	// MaxAttempts 限制同一条消息的处理次数，超过后记为 dead。零值表示五次。
	MaxAttempts int
	// RedisInstance names the redis.<instance> connection block to reuse.
	// RedisInstance 指定复用的 redis.<实例> 连接段。
	RedisInstance string
	// Key is the delayed ZSET key. Required.
	// Key 是延迟 ZSET 的键。必填。
	Key string
	// MaxRetries is how often a failed message is written back with a fresh
	// due time before the queue drops it.
	// MaxRetries 是失败消息以新到期时间写回的次数，超过后队列会丢弃它。
	MaxRetries int
	// PopLimit is the maximum number of due messages taken in one poll.
	// Zero means ten.
	// PopLimit 是单次轮询取出的到期消息上限，0 表示 10。
	PopLimit int
	// IdleDelay is the pause when nothing is due.
	// IdleDelay 是没有到期消息时的等待时间。
	IdleDelay time.Duration
	// RetryDelay is both the wait before a failed message becomes due again
	// and the pause after a Redis error.
	// RetryDelay 既是失败消息重新到期前的等待，也是 Redis 出错后的暂停。
	RetryDelay time.Duration
	// DiscardOnError drops a failed message instead of scheduling a retry.
	// DiscardOnError 打开后，失败消息被丢弃而不是安排重试。
	DiscardOnError bool
	// Handler runs the business branch. Required.
	// Handler 执行业务分支。必填。
	Handler DelayQueuesHandlerFunc
}

// DelayQueuesMessage is one due entry as the handler sees it.
// DelayQueuesMessage 是 handler 看到的一条到期条目。
type DelayQueuesMessage struct {
	Envelope
	// Attempt counts deliveries, one-based.
	// Attempt 是投递次数，从 1 开始。
	Attempt int32
	// Message is the native delayed entry, with its due-derived retry count.
	// Message 是原生延迟条目，带按重试次数递增的计数。
	Message *queues.Message
}

// DelayQueuesHandlerFunc processes one due message.
// DelayQueuesHandlerFunc 处理一条到期消息。
type DelayQueuesHandlerFunc func(ctx context.Context, cfg DelayQueuesConfig, msg DelayQueuesMessage) error

// DelayQueuesConsumer is one delayed subscription, ready for a worker group.
// DelayQueuesConsumer 是一个延迟订阅，可以直接放进 worker 分组。
type DelayQueuesConsumer struct {
	cfg  DelayQueuesConfig
	conn gofyredis.Config
}

// NewDelayQueues validates cfg, resolves its connection block and returns the
// consumer. It does not connect; Run does.
// NewDelayQueues 校验 cfg、解析连接段并返回消费者。它不建连，建连在 Run 里。
func NewDelayQueues(conns Connections, cfg DelayQueuesConfig) (*DelayQueuesConsumer, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("delay queues consumer %q: %w", cfg.Name, err)
	}
	conn, err := conns.ResolveRedis(cfg.RedisInstance)
	if err != nil {
		return nil, fmt.Errorf("delay queues consumer %q: %w", cfg.Name, err)
	}
	return &DelayQueuesConsumer{cfg: cfg, conn: conn}, nil
}

func (c *DelayQueuesConsumer) Name() string { return c.cfg.Name }

func (c *DelayQueuesConsumer) Concurrency() uint8 { return concurrency(c.cfg.Concurrency) }

// ReportsReady tells the runner that Run reports readiness itself: the queue is
// polled inside Run, so "Run was entered" is not "the delayed queue is usable".
// ReportsReady 告诉 runner：Run 自行上报就绪。队列在 Run 内轮询，因此"Run 已进入"
// 不等于"延迟队列可用"。
func (c DelayQueuesConsumer) ReportsReady() {}

// Run owns the subscription: it connects, polls the delayed ZSET until ctx is
// cancelled and releases the client on the way out.
// Run 拥有订阅：建连、轮询延迟 ZSET 到 ctx 取消，并在退出时释放客户端。
func (c *DelayQueuesConsumer) Run(ctx context.Context) error {
	name := c.cfg.Name
	proc, lg, err := start(ctx, name, c.cfg.Idempotency, c.cfg.MaxAttempts, c.handle)
	if err != nil {
		return err
	}

	client, err := gofyredis.New(ctx, c.conn)
	if err != nil {
		return fmt.Errorf("%s: connect: %w", name, err)
	}
	defer func() { _ = client.Close() }()

	queue, err := queues.NewDelayQueue(client, queues.Config{
		Key:        c.cfg.Key,
		MaxRetries: c.cfg.MaxRetries,
	})
	if err != nil {
		return fmt.Errorf("%s: open delayed queue %s: %w", name, c.cfg.Key, err)
	}

	// DelayQueue.Consume has the same signature as Queue.Consume, so the
	// immediate queue's worker adapter drives the delayed one unchanged.
	// DelayQueue.Consume 与 Queue.Consume 签名相同，立即队列的 worker 适配器
	// 因此可以原样驱动延迟队列。
	w, err := queuesconsumer.New(name, queue, queuesconsumer.Config{
		Handler: func(ctx context.Context, msg *queues.Message) error {
			return proc.Handle(ctx, delayQueuesMessage(c.cfg.Key, msg))
		},
		Options: queues.ConsumeOptions{
			IdleDelay:      c.cfg.IdleDelay,
			RetryDelay:     c.cfg.RetryDelay,
			PopLimit:       c.cfg.PopLimit,
			DiscardOnError: c.cfg.DiscardOnError,
			OnError: func(err error) {
				lg.Error("delay queues consume error", zap.Error(err))
			},
			OnReady: func() { gofyworker.Ready(ctx) },
		},
	})
	if err != nil {
		return err
	}

	lg.Info("delay queues consumer started",
		zap.String("key", c.cfg.Key),
		zap.Int("pop_limit", c.cfg.PopLimit),
		zap.Bool("idempotency", c.cfg.Idempotency),
	)
	defer lg.Info("delay queues consumer stopped", zap.String("key", c.cfg.Key))

	return w.Run(ctx)
}

func (c *DelayQueuesConsumer) handle(ctx context.Context, message Message, envelope Envelope) error {
	native, _ := message.Native.(*queues.Message)
	return c.cfg.Handler(ctx, c.cfg, DelayQueuesMessage{
		Envelope: envelope,
		Attempt:  message.Attempt,
		Message:  native,
	})
}

func (c DelayQueuesConfig) withDefaults() DelayQueuesConfig {
	if c.RedisInstance == "" {
		c.RedisInstance = "ratelimit"
	}
	if c.Concurrency == 0 {
		c.Concurrency = 1
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = 3
	}
	if c.PopLimit <= 0 {
		c.PopLimit = 10
	}
	if c.IdleDelay <= 0 {
		c.IdleDelay = 200 * time.Millisecond
	}
	if c.RetryDelay <= 0 {
		c.RetryDelay = 3 * time.Second
	}
	if c.MaxAttempts < 1 {
		c.MaxAttempts = 5
	}
	return c
}

func (c DelayQueuesConfig) validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("name is required")
	}
	if !validName(c.Name) {
		return fmt.Errorf("invalid worker name %q", c.Name)
	}
	if strings.TrimSpace(c.Key) == "" {
		return errors.New("key is required")
	}
	if c.Handler == nil {
		return errors.New("handler is required")
	}
	if err := validateConcurrency(c.Concurrency); err != nil {
		return err
	}
	return nil
}

// delayQueuesMessage reuses the immediate queue's conversion, only changing
// the source so logs and traces tell a due message from a popped one.
// delayQueuesMessage 复用立即队列的转换，只改 source，让日志与 trace 能区分
// 「到期」消息与「弹出」消息。
func delayQueuesMessage(key string, msg *queues.Message) Message {
	out := queuesMessage(key, msg)
	out.Source = "delay_queues"
	return out
}
