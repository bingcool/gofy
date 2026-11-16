package consumer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/bingcool/gofy/support/worker/consumer/rocketmqconsumer"
)

// RocketMQConfig describes one RocketMQ subscription.
//
// A RocketMQ consumer is single-use — created, consumed once, closed — so
// Concurrency stays at one worker instance and throughput comes from
// PullWorkers inside the SDK.
// RocketMQConfig 描述一个 RocketMQ 订阅。
//
// RocketMQ 消费者是一次性对象——创建、消费一次、关闭——因此 worker 实例数保持 1，
// 吞吐由 SDK 内部的 PullWorkers 提供。
type RocketMQConfig struct {
	// Name is the worker name and the idempotency scope. Required.
	// Name 是 worker 名与幂等作用域。必填。
	Name string
	// Concurrency is the number of consumer instances in this process.
	// Concurrency 是进程内的消费者实例数。
	Concurrency int
	// Idempotency turns a redelivery into a no-op. Needs a database.
	// Idempotency 把重复投递变成空操作。需要数据库。
	Idempotency bool
	// MaxAttempts bounds how often one message is handled before it is parked
	// as dead. Zero means five.
	// MaxAttempts 限制同一条消息的处理次数，超过后记为 dead。零值表示五次。
	MaxAttempts int
	// Instance names the rocketmq.<instance> connection block. Empty means
	// "default".
	// Instance 指定 rocketmq.<实例> 连接段。为空表示 "default"。
	Instance string
	// Topic and Group are the subscription. Both are required.
	// Topic 与 Group 是订阅本身。两者都必填。
	Topic string
	Group string
	// Tags is an OR list — the SDK joins the entries with "||" into one
	// subscription expression. Empty, or a single "*", subscribes to every tag.
	// Tags 是"或"关系：SDK 用 "||" 把各元素拼成一个订阅表达式。留空或只有一个
	// "*" 表示订阅全部 tag。
	Tags []string
	// PullWorkers is the SDK's receive concurrency, not the worker's.
	// PullWorkers 是 SDK 的拉取并发，不是 worker 的实例数。
	PullWorkers int
	// BatchSize is how many messages one pull asks for.
	// BatchSize 是一次拉取请求的消息条数。
	BatchSize int
	// AwaitDuration is how long a pull waits on the broker side.
	// AwaitDuration 是拉取在 broker 侧的等待时长。
	AwaitDuration time.Duration
	// InvisibleDuration is the redelivery timeout: a message not acknowledged
	// within it becomes visible again.
	// InvisibleDuration 是重投超时：在此时间内未确认的消息会重新可见。
	InvisibleDuration time.Duration
	// DiscardOnError, when set, makes the adapter ack and move on instead of
	// stopping on a handler error.
	// DiscardOnError 打开后，适配器遇到 handler 错误会确认并继续，而不是停下。
	DiscardOnError bool
	// Handler runs the business branch. Required.
	// Handler 执行业务分支。必填。
	Handler RocketMQHandlerFunc
}

// RocketMQMessage is one message as the handler sees it: the decoded envelope,
// the delivery count and the native message.
//
// Message carries what the envelope does not — the broker message id, keys,
// tags, properties and the invisible-duration deadline — which is what a
// handler needs to log or to act on outside the automatic acknowledgement.
// RocketMQMessage 是 handler 看到的单条消息：解码后的信封、投递次数与原生消息。
//
// Message 提供信封里没有的东西——broker 消息 ID、keys、tags、属性以及不可见时长
// 期限——handler 记日志或在自动确认之外做事时需要它们。
type RocketMQMessage struct {
	Envelope
	// Attempt is the broker's own delivery counter, the most trustworthy
	// attempt number of the four protocols.
	// Attempt 是 broker 自己的投递计数，是四种协议里最可信的尝试次数。
	Attempt int32
	// Message is the native message.
	// Message 是原生消息。
	Message *gofyrocketmq.Message
}

// RocketMQHandlerFunc processes one RocketMQ message.
//
// cfg carries topic, group and tags, which is what a handler needs to tell two
// subscriptions of the same topic apart without closing over the consumer.
// RocketMQHandlerFunc 处理一条 RocketMQ 消息。
//
// cfg 带 topic、group 与 tags：handler 想区分同一 topic 的两个订阅时就靠它，不必闭包
// 捕获消费者。
type RocketMQHandlerFunc func(ctx context.Context, cfg RocketMQConfig, msg RocketMQMessage) error

// RocketMQConsumer is one subscription, ready to be put in a worker group.
// RocketMQConsumer 是一个订阅，可以直接放进 worker 分组。
type RocketMQConsumer struct {
	cfg  RocketMQConfig
	conn gofyrocketmq.Config
}

// NewRocketMQ validates cfg, resolves its connection block and returns the
// consumer. It does not connect; Run does.
// NewRocketMQ 校验 cfg、解析它的连接段并返回消费者。它不建连，建连在 Run 里。
func NewRocketMQ(conns Connections, cfg RocketMQConfig) (*RocketMQConsumer, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("rocketmq consumer %q: %w", cfg.Name, err)
	}
	conn, err := conns.ResolveRocketMQ(cfg.Instance)
	if err != nil {
		return nil, fmt.Errorf("rocketmq consumer %q: %w", cfg.Name, err)
	}
	return &RocketMQConsumer{cfg: cfg, conn: conn}, nil
}

// Name returns the worker name.
// Name 返回 worker 名。
func (c *RocketMQConsumer) Name() string { return c.cfg.Name }

// ReportsReady tells the runner that Run reports readiness itself: this
// consumer connects and subscribes inside Run, so "Run was entered" is not the
// same as "the process can accept work".
// ReportsReady 告诉 runner：Run 自行上报就绪。该消费者在 Run 内建连并订阅，因此
// "Run 已进入"并不等于"进程可以接活"。
func (c RocketMQConsumer) ReportsReady() {}

// Concurrency returns how many instances the worker should run.
// Concurrency 返回 worker 应当运行的实例数。
func (c *RocketMQConsumer) Concurrency() uint8 { return concurrency(c.cfg.Concurrency) }

// Run owns the subscription: it connects, consumes until ctx is cancelled and
// releases the consumer on the way out.
// Run 拥有订阅本身：建连、消费到 ctx 取消，并在退出时释放消费者。
func (c *RocketMQConsumer) Run(ctx context.Context) error {
	name := c.cfg.Name
	proc, lg, err := start(ctx, name, c.cfg.Idempotency, c.cfg.MaxAttempts, c.handle)
	if err != nil {
		return err
	}

	consumer, err := gofyrocketmq.NewConsumer(c.conn, gofyrocketmq.ConsumerConfig{
		Topic:             c.cfg.Topic,
		Group:             c.cfg.Group,
		Tags:              c.cfg.Tags,
		BatchSize:         c.cfg.BatchSize,
		Concurrency:       c.cfg.PullWorkers,
		AwaitDuration:     c.cfg.AwaitDuration,
		InvisibleDuration: c.cfg.InvisibleDuration,
	})
	if err != nil {
		return fmt.Errorf("%s: subscribe %s: %w", name, c.cfg.Topic, err)
	}
	// The adapter closes on shutdown; this close covers the restart path.
	// 适配器在停机时关闭；这里覆盖重启路径。
	defer func() { _ = consumer.Close() }()

	w, err := rocketmqconsumer.New(name, consumer, rocketmqconsumer.Config{
		Handler: func(ctx context.Context, message *gofyrocketmq.Message) error {
			return proc.Handle(ctx, rocketmqMessage(message))
		},
		Options: gofyrocketmq.ConsumeOptions{
			DiscardOnError: c.cfg.DiscardOnError,
			OnError: func(err error) {
				lg.Error("rocketmq consume error", zap.Error(err))
			},
			// Ready comes from the adapter after the SDK consumer started and
			// registered the subscription with the broker.
			// 就绪由适配器在 SDK 消费者启动、订阅注册到 broker 之后上报。
			OnReady: func() { gofyworker.Ready(ctx) },
		},
	})
	if err != nil {
		return err
	}

	lg.Info("rocketmq consumer started",
		zap.String("topic", c.cfg.Topic),
		zap.String("group", c.cfg.Group),
		zap.Strings("tags", c.cfg.Tags),
		zap.Bool("idempotency", c.cfg.Idempotency),
	)
	defer lg.Info("rocketmq consumer stopped", zap.String("topic", c.cfg.Topic))

	return w.Run(ctx)
}

// handle adapts the shared chain's callback to the typed handler.
// handle 把共用链路的回调适配成有类型的 handler。
func (c *RocketMQConsumer) handle(ctx context.Context, message Message, envelope Envelope) error {
	native, _ := message.Native.(*gofyrocketmq.Message)
	return c.cfg.Handler(ctx, c.cfg, RocketMQMessage{
		Envelope: envelope,
		Attempt:  message.Attempt,
		Message:  native,
	})
}

func (c RocketMQConfig) withDefaults() RocketMQConfig {
	if c.Instance == "" {
		c.Instance = "default"
	}
	if c.Concurrency == 0 {
		c.Concurrency = 1
	}
	if c.PullWorkers < 1 {
		c.PullWorkers = 1
	}
	if c.BatchSize < 1 {
		c.BatchSize = 16
	}
	if c.AwaitDuration <= 0 {
		c.AwaitDuration = 5 * time.Second
	}
	if c.InvisibleDuration <= 0 {
		c.InvisibleDuration = 30 * time.Second
	}
	if c.MaxAttempts < 1 {
		c.MaxAttempts = 5
	}
	// Tags are joined verbatim into the subscription expression, so a stray
	// space would turn "order" into a tag the broker never matches. Trim once
	// here, where the value is still ours, instead of in every call site.
	// tag 会原样拼进订阅表达式，多一个空格就会让 "order" 变成 broker 永远匹配不上的
	// tag。在值还归我们管的时候统一去一次首尾空格，而不是留给每个调用点。
	if len(c.Tags) > 0 {
		tags := make([]string, len(c.Tags))
		for i, tag := range c.Tags {
			tags[i] = strings.TrimSpace(tag)
		}
		c.Tags = tags
	}
	return c
}

func (c RocketMQConfig) validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("name is required")
	}
	if !validName(c.Name) {
		return fmt.Errorf("invalid worker name %q", c.Name)
	}
	if strings.TrimSpace(c.Topic) == "" {
		return errors.New("topic is required")
	}
	if strings.TrimSpace(c.Group) == "" {
		return errors.New("group is required")
	}
	if c.Handler == nil {
		return errors.New("handler is required")
	}
	// An empty tag or one carrying the "||" separator would rewrite the
	// subscription expression the SDK builds from this slice. The SDK rejects
	// both when the consumer is created inside Run; checking here makes it a
	// registration-time error, before any goroutine exists.
	// 空 tag 或自身含 "||" 的 tag 会改写 SDK 用这个切片拼出的订阅表达式。SDK 在 Run 里
	// 创建消费者时才拒绝这两种情况；在这里检查，它就成了注册阶段的错误，早于任何
	// goroutine。
	for _, tag := range c.Tags {
		if tag == "" || strings.Contains(tag, "||") {
			return fmt.Errorf("invalid tag %q: tags are joined with %q, so an element must be non-empty and must not contain it", tag, "||")
		}
	}
	if err := validateConcurrency(c.Concurrency); err != nil {
		return err
	}
	return nil
}

// rocketmqMessage converts one RocketMQ message into the shared view.
//
// DeliveryAttempt is the broker's own counter and is the most trustworthy
// attempt number of the four protocols, which makes it useful for spotting a
// message that keeps failing.
// rocketmqMessage 把一条 RocketMQ 消息转成共用视图。
//
// DeliveryAttempt 是 broker 自己的计数器，是四种协议里最可信的尝试次数，因此
// 适合用来发现一条持续失败的消息。
func rocketmqMessage(message *gofyrocketmq.Message) Message {
	return Message{
		ID:       rocketmqMessageID(message),
		Source:   "rocketmq",
		Topic:    message.Topic,
		Key:      strings.Join(message.Keys, ","),
		Body:     message.Body,
		Attempt:  message.DeliveryAttempt,
		Headers:  message.Properties,
		Enqueued: message.BornTimestamp,
		Native:   message,
	}
}

// rocketmqMessageID prefers the broker message id and falls back to the queue
// offset.
// rocketmqMessageID 优先用 broker 的消息 ID，缺失时回退到队列位点。
func rocketmqMessageID(message *gofyrocketmq.Message) string {
	if id := message.MessageID; id != "" {
		return id
	}
	return fmt.Sprintf("%s@%s", message.Topic, strconv.FormatInt(message.Offset, 10))
}
