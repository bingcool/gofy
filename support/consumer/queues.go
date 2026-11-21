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
	gofyworker "github.com/bingcool/gofy/worker"
	"github.com/bingcool/gofy/worker/consumer/queuesconsumer"
)

// QueuesConfig describes one Redis queue subscription.
//
// This is the cheapest of the four to run and the weakest of the four
// guarantees: a popped message is already removed from Redis, so a crash in the
// handler loses it. Use Kafka or AMQP when that is unacceptable.
// QueuesConfig 描述一个 Redis 队列订阅。
//
// 这是四种里运行成本最低、保证也最弱的一种：消息弹出时已从 Redis 删除，handler
// 崩溃就会丢这一条。不能接受的场景请用 Kafka 或 AMQP。
type QueuesConfig struct {
	// Name is the worker name and the idempotency scope. Required.
	// Name 是 worker 名与幂等作用域。必填。
	Name string
	// Concurrency can be raised freely: every instance pops independently and
	// the pop itself is atomic.
	// Concurrency 可以放心调大：每个实例独立弹出，弹出本身是原子的。
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
	// Key is the queue name and the Redis key. Required.
	// Key 是队列名，也是 Redis 键。必填。
	Key string
	// MaxRetries is how often a failed message re-enters the main queue before
	// the queue drops it. Duplicates deferred as in flight do not consume this
	// budget.
	// MaxRetries 是失败消息重新进入主队列的次数，超过后队列会丢弃它。在途延后的
	// 重复投递不消耗这个预算。
	MaxRetries int
	// IdleDelay is the pause after an empty pop.
	// IdleDelay 是空弹出后的等待时间。
	IdleDelay time.Duration
	// RetryDelay is how long a failed message waits before it is re-queued.
	// RetryDelay 是失败消息重新入队前的等待时间。
	RetryDelay time.Duration
	// DiscardOnError, when set, drops a failed message instead of re-queueing
	// it. In-flight duplicates are still deferred, never discarded.
	// DiscardOnError 打开后，失败消息会被丢弃而不是重新入队。在途重复投递仍会
	// 延后重投，不会被丢弃。
	DiscardOnError bool
	// Handler runs the business branch. Required.
	// Handler 执行业务分支。必填。
	Handler QueuesHandlerFunc
}

// QueuesMessage is one queue entry as the handler sees it: the decoded
// envelope, the delivery count and the native message.
//
// Message carries what the queue keeps on the entry — its id, retry number,
// timestamp and the raw payload — which is what a handler needs to log or to
// re-queue by hand.
// QueuesMessage 是 handler 看到的单条队列项：解码后的信封、投递次数与原生消息。
//
// Message 提供队列留在条目上的东西——ID、重试次数、时间戳与原始 payload——handler
// 记日志或手工重新入队时需要它们。
type QueuesMessage struct {
	Envelope
	// Attempt counts deliveries, one-based: RetryNum is zero-based because the
	// queue counts retries.
	// Attempt 是投递次数，从 1 开始：RetryNum 从 0 开始，因为队列计的是重试次数。
	Attempt int32
	// Message is the native entry.
	// Message 是原生条目。
	Message *queues.Message
}

// QueuesHandlerFunc processes one queue entry.
//
// cfg is the subscription — key, concurrency, retry budget — passed by value so
// the handler can log which queue an entry came from without a closure.
// QueuesHandlerFunc 处理一条队列项。
//
// cfg 是这个订阅——key、并发、重试预算——按值传入，handler 因此不用闭包，也能记下
// 这条来自哪个队列。
type QueuesHandlerFunc func(ctx context.Context, cfg QueuesConfig, msg QueuesMessage) error

// QueuesConsumer is one queue subscription, ready to be put in a worker group.
// QueuesConsumer 是一个队列订阅，可以直接放进 worker 分组。
type QueuesConsumer struct {
	cfg  QueuesConfig
	conn gofyredis.Config
}

// ErrInFlight must stay recognizable as a queues in-flight error; that marker
// is what defers the copy instead of consuming its retry budget.
// ErrInFlight 必须始终满足 queues 的在途错误标记；正是这个标记让副本延后重投，
// 而不是消耗重试预算。
var _ queues.InFlightError = ErrInFlight

// NewQueues validates cfg, resolves its connection block and returns the
// consumer. It does not connect; Run does.
// NewQueues 校验 cfg、解析它的连接段并返回消费者。它不建连，建连在 Run 里。
func NewQueues(conns Connections, cfg QueuesConfig) (*QueuesConsumer, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("queues consumer %q: %w", cfg.Name, err)
	}
	conn, err := conns.ResolveRedis(cfg.RedisInstance)
	if err != nil {
		return nil, fmt.Errorf("queues consumer %q: %w", cfg.Name, err)
	}
	return &QueuesConsumer{cfg: cfg, conn: conn}, nil
}

// Name returns the worker name.
// Name 返回 worker 名。
func (c *QueuesConsumer) Name() string { return c.cfg.Name }

// ReportsReady tells the runner that Run reports readiness itself: this
// consumer connects and subscribes inside Run, so "Run was entered" is not the
// same as "the process can accept work".
// ReportsReady 告诉 runner：Run 自行上报就绪。该消费者在 Run 内建连并订阅，因此
// "Run 已进入"并不等于"进程可以接活"。
func (c QueuesConsumer) ReportsReady() {}

// RequiresDefaultDB reports the ledger's dependency on db.default: with
// idempotency on, this consumer cannot run without the framework-managed pool.
// RequiresDefaultDB 报告台账对 db.default 的依赖：幂等开启时，该消费者离不开框架
// 托管的连接池。
func (c QueuesConsumer) RequiresDefaultDB() bool { return c.cfg.Idempotency }

// Concurrency returns how many instances the worker should run.
// Concurrency 返回 worker 应当运行的实例数。
func (c *QueuesConsumer) Concurrency() uint8 { return concurrency(c.cfg.Concurrency) }

// Run owns the subscription: it connects, pops until ctx is cancelled and
// releases the client on the way out.
// Run 拥有订阅本身：建连、弹出到 ctx 取消，并在退出时释放客户端。
func (c *QueuesConsumer) Run(ctx context.Context) error {
	name := c.cfg.Name
	proc, lg, err := start(ctx, name, c.cfg.Idempotency, c.cfg.MaxAttempts, []string{c.cfg.Key}, c.handle)
	if err != nil {
		return err
	}

	client, err := gofyredis.New(ctx, c.conn)
	if err != nil {
		proc.metrics.recordConnectError("queues", "connect")
		return fmt.Errorf("%s: connect: %w", name, err)
	}
	defer func() { _ = client.Close() }()

	queue, err := queues.NewQueue(client, queues.Config{
		Key:        c.cfg.Key,
		MaxRetries: c.cfg.MaxRetries,
	})
	if err != nil {
		proc.metrics.recordConnectError("queues", "subscribe")
		return fmt.Errorf("%s: open queue %s: %w", name, c.cfg.Key, err)
	}

	w, err := queuesconsumer.New(name, queue, queuesconsumer.Config{
		Handler: func(ctx context.Context, msg *queues.Message) error {
			return proc.Handle(ctx, queuesMessage(c.cfg.Key, msg))
		},
		Options: queues.ConsumeOptions{
			IdleDelay:  c.cfg.IdleDelay,
			RetryDelay: c.cfg.RetryDelay,
			// A duplicate delivery that is still being processed elsewhere
			// re-enters the queue only after the processing lease expires.
			// 另一实例仍在处理的重复投递，要等处理租约到期后才重新入队。
			InFlightDelay:  DefaultProcessingLease,
			DiscardOnError: c.cfg.DiscardOnError,
			OnError: func(err error) {
				lg.Error("queues consume error", zap.Error(err))
			},
			OnNackError: func(error) { proc.metrics.recordBrokerError("queues", "nack") },
			// Ready comes from the first successful pop: a lazy Redis client
			// proves nothing until a command round-trips.
			// 就绪来自第一次成功弹出：惰性 Redis 客户端在一条命令往返之前什么都证明不了。
			OnReady: func() { gofyworker.Ready(ctx) },
		},
	})
	if err != nil {
		return err
	}

	lg.Info("queues consumer started",
		zap.String("queue", c.cfg.Key),
		zap.Int("max_retries", c.cfg.MaxRetries),
		zap.Bool("idempotency", c.cfg.Idempotency),
	)
	defer lg.Info("queues consumer stopped", zap.String("queue", c.cfg.Key))

	return w.Run(ctx)
}

// handle adapts the shared chain's callback to the typed handler.
// handle 把共用链路的回调适配成有类型的 handler。
func (c *QueuesConsumer) handle(ctx context.Context, message Message, envelope Envelope) error {
	native, _ := message.Native.(*queues.Message)
	return c.cfg.Handler(ctx, c.cfg, QueuesMessage{
		Envelope: envelope,
		Attempt:  message.Attempt,
		Message:  native,
	})
}

func (c QueuesConfig) withDefaults() QueuesConfig {
	if c.RedisInstance == "" {
		c.RedisInstance = "ratelimit"
	}
	if c.Concurrency == 0 {
		c.Concurrency = 1
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = 3
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

func (c QueuesConfig) validate() error {
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

// queuesMessage converts one Redis queue message into the shared view.
//
// The queue stamps every message with its own id, so deduplication works
// without asking the producer for anything.
// queuesMessage 把一条 Redis 队列消息转成共用视图。
//
// 队列会给每条消息盖自己的 ID，因此去重不需要生产者配合。
func queuesMessage(key string, msg *queues.Message) Message {
	attempt := int32(msg.RetryNum) + 1
	var enqueued time.Time
	if msg.Timestamp > 0 {
		enqueued = time.Unix(msg.Timestamp, 0)
	}
	return Message{
		ID:       msg.ID,
		Source:   "queues",
		Topic:    key,
		Body:     msg.Payload,
		Attempt:  attempt,
		Enqueued: enqueued,
		Native:   msg,
	}
}
