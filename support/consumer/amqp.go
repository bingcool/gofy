package consumer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	gofyamqp "github.com/bingcool/gofy/support/amqp"
	"github.com/bingcool/gofy/support/worker/consumer/amqpconsumer"
)

// AMQPConfig describes one RabbitMQ queue subscription.
//
// The topology is declared by the consumer, so the exchange, queue and binding
// exist before the first message is published to them. Every field defaults to
// something sane; a consumer file usually sets Name, Exchange, Queue,
// BindingKey and Handler.
// AMQPConfig 描述一个 RabbitMQ 队列订阅。
//
// topology 由消费者声明，因此 exchange、队列与绑定在第一条消息发布之前就已存在。
// 每个字段都有合理默认值；消费者文件通常只写 Name、Exchange、Queue、BindingKey 与
// Handler。
type AMQPConfig struct {
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
	// Instance names the amqp.<instance> connection block. Empty means
	// "default".
	// Instance 指定 amqp.<实例> 连接段。为空表示 "default"。
	Instance string
	// Exchange, ExchangeType, Queue, BindingKey and RoutingKey mirror
	// amqp.Topology. A direct exchange requires RoutingKey == BindingKey; an
	// empty RoutingKey is copied from BindingKey.
	// Exchange、ExchangeType、Queue、BindingKey、RoutingKey 对应
	// amqp.Topology。direct exchange 要求 RoutingKey == BindingKey；RoutingKey
	// 为空时会从 BindingKey 复制。
	Exchange     string
	ExchangeType string
	Queue        string
	BindingKey   string
	RoutingKey   string
	// NonDurable declares a transient queue instead of a durable one. The flag
	// is inverted on purpose: durable is the right default for a business
	// queue, and a plain bool would have made the zero value mean "lose the
	// queue on restart".
	// NonDurable 声明一个临时队列而不是持久化队列。这里刻意用反向标志：业务队列的正确
	// 默认是持久化，而普通 bool 的零值会变成"重启即丢队列"。
	NonDurable bool
	// PrefetchCount is the unacknowledged delivery window. Raising it is
	// cheaper than adding consumer instances.
	// PrefetchCount 是未确认投递的窗口。调大它比增加消费者实例更划算。
	PrefetchCount int
	// AutoAck disables manual acknowledgement; the handler result then only
	// affects logging, not redelivery.
	// AutoAck 关闭手动确认，此时 handler 结果只影响日志，不影响重投。
	AutoAck bool
	// DiscardOnError, when set, makes the adapter ack and move on instead of
	// stopping on a handler error.
	// DiscardOnError 打开后，适配器遇到 handler 错误会确认并继续，而不是停下。
	DiscardOnError bool
	// ReconnectDelay is the pause before reconnecting after a broker failure.
	// ReconnectDelay 是 broker 故障后重连前的等待时间。
	ReconnectDelay time.Duration
	// Handler runs the business branch. Required.
	// Handler 执行业务分支。必填。
	Handler AMQPHandlerFunc
}

// AMQPMessage is one delivery as the handler sees it: the decoded envelope, the
// delivery count and the native delivery.
//
// Delivery carries what the envelope does not — the delivery tag (what
// Delivery.Ack/Nack take), the routing key the message actually had, the
// redelivered flag — which is what a handler needs to acknowledge manually with
// AutoAck off.
// AMQPMessage 是 handler 看到的单次投递：解码后的信封、投递次数与原生投递。
//
// Delivery 提供信封里没有的东西——投递标签（Delivery.Ack/Nack 的入参）、消息实际带的
// routing key、重投标志——关闭 AutoAck 后要手动确认时，handler 需要的正是它们。
type AMQPMessage struct {
	Envelope
	// Attempt is 1 on first delivery and 2 once the broker marks the delivery
	// redelivered; AMQP exposes a flag, not a count, and that is all the ledger
	// needs.
	// Attempt 第一次投递为 1，broker 标记重投后为 2；AMQP 只提供标志而非次数，这对
	// 台账已经足够。
	Attempt int32
	// Delivery is the native delivery.
	// Delivery 是原生投递。
	Delivery amqp091.Delivery
}

// AMQPHandlerFunc processes one AMQP delivery.
//
// cfg is this subscription's config — exchange, queue, binding key — passed by
// value so the handler can log or branch on it without a closure over the
// consumer.
// AMQPHandlerFunc 处理一次 AMQP 投递。
//
// cfg 是这个订阅的配置——exchange、queue、binding key——按值传入，handler 因此不用
// 闭包捕获消费者，也能就它记日志或分支。
type AMQPHandlerFunc func(ctx context.Context, cfg AMQPConfig, msg AMQPMessage) error

// AMQPConsumer is one queue subscription, ready to be put in a worker group.
// AMQPConsumer 是一个队列订阅，可以直接放进 worker 分组。
type AMQPConsumer struct {
	cfg  AMQPConfig
	conn gofyamqp.Config
}

// NewAMQP validates cfg, resolves its connection block and returns the
// consumer. It does not connect; Run does.
// NewAMQP 校验 cfg、解析它的连接段并返回消费者。它不建连，建连在 Run 里。
func NewAMQP(conns Connections, cfg AMQPConfig) (*AMQPConsumer, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("amqp consumer %q: %w", cfg.Name, err)
	}
	conn, err := conns.ResolveAMQP(cfg.Instance)
	if err != nil {
		return nil, fmt.Errorf("amqp consumer %q: %w", cfg.Name, err)
	}
	return &AMQPConsumer{cfg: cfg, conn: conn}, nil
}

// Name returns the worker name.
// Name 返回 worker 名。
func (c *AMQPConsumer) Name() string { return c.cfg.Name }

// Concurrency returns how many instances the worker should run.
// Concurrency 返回 worker 应当运行的实例数。
func (c *AMQPConsumer) Concurrency() uint8 { return concurrency(c.cfg.Concurrency) }

// Run owns the subscription: it connects, declares the topology, consumes until
// ctx is cancelled and releases the connection on the way out.
// Run 拥有订阅本身：建连、声明 topology、消费到 ctx 取消，并在退出时释放连接。
func (c *AMQPConsumer) Run(ctx context.Context) error {
	name := c.cfg.Name
	proc, lg, err := start(ctx, name, c.cfg.Idempotency, c.cfg.MaxAttempts, c.handle)
	if err != nil {
		return err
	}

	client, err := gofyamqp.New(ctx, c.conn)
	if err != nil {
		return fmt.Errorf("%s: connect: %w", name, err)
	}
	defer func() { _ = client.Close() }()

	queue, err := gofyamqp.NewQueue(client, gofyamqp.QueueConfig{
		Topology: gofyamqp.Topology{
			Exchange:     c.cfg.Exchange,
			ExchangeType: c.cfg.ExchangeType,
			Queue:        c.cfg.Queue,
			BindingKey:   c.cfg.BindingKey,
			RoutingKey:   c.cfg.RoutingKey,
			Durable:      !c.cfg.NonDurable,
		},
	})
	if err != nil {
		return fmt.Errorf("%s: declare queue %s: %w", name, c.cfg.Queue, err)
	}
	// Closed twice for the same reason as Kafka: the adapter keeps the queue
	// open across a restart, while this consumer rebuilds it.
	// 与 Kafka 同理关闭两次：适配器为重启保留队列，而本实现会重建它。
	defer func() { _ = queue.Close() }()

	w, err := amqpconsumer.New(name, queue, amqpconsumer.Config{
		Handler: func(ctx context.Context, delivery amqp091.Delivery) error {
			return proc.Handle(ctx, amqpMessage(delivery))
		},
		Options: gofyamqp.ConsumeOptions{
			PrefetchCount:  c.cfg.PrefetchCount,
			AutoAck:        c.cfg.AutoAck,
			DiscardOnError: c.cfg.DiscardOnError,
			ReconnectDelay: c.cfg.ReconnectDelay,
			OnError: func(err error) {
				lg.Error("amqp consume error", zap.Error(err))
			},
		},
	})
	if err != nil {
		return err
	}

	lg.Info("amqp consumer started",
		zap.String("exchange", c.cfg.Exchange),
		zap.String("queue", c.cfg.Queue),
		zap.Int("prefetch", c.cfg.PrefetchCount),
		zap.Bool("idempotency", c.cfg.Idempotency),
	)
	defer lg.Info("amqp consumer stopped", zap.String("queue", c.cfg.Queue))

	return w.Run(ctx)
}

// handle adapts the shared chain's callback to the typed handler.
// handle 把共用链路的回调适配成有类型的 handler。
func (c *AMQPConsumer) handle(ctx context.Context, message Message, envelope Envelope) error {
	delivery, _ := message.Native.(amqp091.Delivery)
	return c.cfg.Handler(ctx, c.cfg, AMQPMessage{
		Envelope: envelope,
		Attempt:  message.Attempt,
		Delivery: delivery,
	})
}

func (c AMQPConfig) withDefaults() AMQPConfig {
	if c.Instance == "" {
		c.Instance = "default"
	}
	if c.Concurrency == 0 {
		c.Concurrency = 1
	}
	if c.ExchangeType == "" {
		c.ExchangeType = "direct"
	}
	if c.PrefetchCount == 0 {
		c.PrefetchCount = 32
	}
	if c.ReconnectDelay <= 0 {
		c.ReconnectDelay = 3 * time.Second
	}
	if c.MaxAttempts < 1 {
		c.MaxAttempts = 5
	}
	return c
}

func (c AMQPConfig) validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("name is required")
	}
	if !validName(c.Name) {
		return fmt.Errorf("invalid worker name %q", c.Name)
	}
	if strings.TrimSpace(c.Exchange) == "" {
		return errors.New("exchange is required")
	}
	if strings.TrimSpace(c.Queue) == "" {
		return errors.New("queue is required")
	}
	if c.Handler == nil {
		return errors.New("handler is required")
	}
	if err := validateConcurrency(c.Concurrency); err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(c.ExchangeType)) {
	case "direct", "fanout", "topic", "headers":
	default:
		return fmt.Errorf("exchange_type must be direct, fanout, topic or headers, got %q", c.ExchangeType)
	}
	if strings.EqualFold(c.ExchangeType, "direct") {
		routingKey, bindingKey := strings.TrimSpace(c.RoutingKey), strings.TrimSpace(c.BindingKey)
		if routingKey == "" && bindingKey == "" {
			return errors.New("direct exchange requires routing_key or binding_key")
		}
		if routingKey != "" && bindingKey != "" && routingKey != bindingKey {
			return errors.New("direct exchange requires routing_key == binding_key")
		}
	}
	if c.PrefetchCount < 0 {
		return errors.New("prefetch_count must not be negative")
	}
	return nil
}

// amqpMessage converts one delivery into the shared message view.
// amqpMessage 把一次投递转成共用的消息视图。
func amqpMessage(delivery amqp091.Delivery) Message {
	headers := amqpHeaders(delivery.Headers)
	attempt := int32(1)
	if delivery.Redelivered {
		// AMQP only exposes a flag, not a count; a redelivery is at least the
		// second attempt, which is all the ledger needs to know.
		// AMQP 只提供重投标志而非次数；重投至少是第二次，这对台账已经足够。
		attempt = 2
	}
	return Message{
		ID:       amqpMessageID(delivery, headers),
		Source:   "amqp",
		Topic:    delivery.RoutingKey,
		Key:      delivery.MessageId,
		Body:     delivery.Body,
		Attempt:  attempt,
		Headers:  headers,
		Enqueued: delivery.Timestamp,
		Native:   delivery,
	}
}

// amqpMessageID prefers the broker-provided message id.
//
// An empty result is intentional: the processor then generates a random id,
// which disables deduplication for that message instead of collapsing two
// different messages published under the same tag. Producers that care about
// redelivery must set MessageId.
// amqpMessageID 优先使用 broker 提供的消息 ID。
//
// 返回空值是刻意保留的行为：处理器会补一个随机 ID，使这条消息不做去重，而不是把
// 两条不同的消息当成同一条。在意重投的生产者必须设置 MessageId。
func amqpMessageID(delivery amqp091.Delivery, headers map[string]string) string {
	if id := delivery.MessageId; id != "" {
		return id
	}
	if id := headers["message_id"]; id != "" {
		return id
	}
	return headers["X-Correlation-Id"]
}

// amqpHeaders flattens the broker table into strings, keeping the traceparent
// readable for the propagator.
// amqpHeaders 把 broker 的 table 压成字符串，同时让传播器能读到 traceparent。
func amqpHeaders(table amqp091.Table) map[string]string {
	if len(table) == 0 {
		return nil
	}
	out := make(map[string]string, len(table))
	for key, value := range table {
		switch v := value.(type) {
		case nil:
			continue
		case string:
			out[key] = v
		case []byte:
			out[key] = string(v)
		default:
			out[key] = fmt.Sprint(v)
		}
	}
	return out
}
