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

// DelayAMQPConfig describes one RabbitMQ delayed subscription.
//
// A delayed Queue has two topologies instead of one. The Delay* fields name
// the staging exchange/queue that holds a message while its TTL runs; Exchange,
// Queue and their keys name the target that receives it once the broker
// dead-letters it. The consumer reads from the target — the staging queue has
// no consumer at all.
//
// TTL is both the default delay and the upper bound for a producer's
// PublishOptions.Delay, because RabbitMQ applies the smaller of the queue TTL
// and the per-message TTL. Keep one delay per staging queue: the broker only
// checks the head of a queue for expiry, so mixing delays lets a long wait
// hold up shorter ones behind it.
//
// DelayAMQPConfig 描述一个 RabbitMQ 延迟订阅。
//
// 延迟队列有两套拓扑而不是一套。Delay* 字段是消息等待 TTL 的暂存 exchange/queue；
// Exchange、Queue 及其 key 是 broker 死信转发后的目标。消费者读的是目标队列，暂存
// 队列上完全没有消费者。
//
// TTL 既是默认延迟，也是生产端 PublishOptions.Delay 的上限——RabbitMQ 取队列 TTL
// 与单条消息 TTL 中较小的那个。一个暂存队列只放一种延迟：broker 只在队头判定过期，
// 混用延迟会让前面的长等待挡住后面的短等待。
type DelayAMQPConfig struct {
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
	// DelayExchange, DelayExchangeType, DelayQueue, DelayBindingKey and
	// DelayRoutingKey describe the staging topology. Required: exchange and
	// queue, plus one key for a direct exchange.
	// DelayExchange、DelayExchangeType、DelayQueue、DelayBindingKey 与
	// DelayRoutingKey 描述暂存拓扑。exchange 与 queue 必填，direct exchange 还需一个 key。
	DelayExchange     string
	DelayExchangeType string
	DelayQueue        string
	DelayBindingKey   string
	DelayRoutingKey   string
	// Exchange, ExchangeType, Queue, BindingKey and RoutingKey describe the
	// target topology that expired messages are routed to. The dead-letter
	// routing key is this topology's RoutingKey (or BindingKey when empty).
	// Exchange、ExchangeType、Queue、BindingKey 与 RoutingKey 描述过期消息被转发到
	// 的目标拓扑。死信 routing key 取该拓扑的 RoutingKey（为空时取 BindingKey）。
	Exchange     string
	ExchangeType string
	Queue        string
	BindingKey   string
	RoutingKey   string
	// TTL is the queue-level TTL, at least 1ms. Required.
	// TTL 是队列级 TTL，至少 1ms。必填。
	TTL time.Duration
	// PreserveRoutingKey keeps each message's original routing key when the
	// broker dead-letters it, instead of forcing one fixed key. Required for a
	// topic target: the routing key is what selects the binding, and a fixed
	// value collapses topic routing (or misses the binding and drops the message).
	// PreserveRoutingKey 让 broker 死信时保留每条消息原始的 routing key，而不是强制改成
	// 一个固定 key。目标是 topic exchange 时必须开启：绑定正是由 routing key 选中的，
	// 固定值会让 topic 分发失效，甚至匹配不上绑定而丢消息。
	PreserveRoutingKey bool
	// NonDurable declares transient queues instead of durable ones.
	// NonDurable 声明临时队列而不是持久化队列。
	NonDurable bool
	// PrefetchCount is the unacknowledged delivery window.
	// PrefetchCount 是未确认投递的窗口。
	PrefetchCount int
	// AutoAck disables manual acknowledgement.
	// AutoAck 关闭手动确认。
	AutoAck bool
	// DiscardOnError acks and moves on instead of requeueing a failed message.
	// DiscardOnError 打开后，handler 失败会确认并继续，而不是重入队。
	DiscardOnError bool
	// ReconnectDelay is the pause before reconnecting after a broker failure.
	// ReconnectDelay 是 broker 故障后重连前的等待时间。
	ReconnectDelay time.Duration
	// Handler runs the business branch. Required.
	// Handler 执行业务分支。必填。
	Handler DelayAMQPHandlerFunc
}

// DelayAMQPMessage is one delivery as the handler sees it. The broker has
// already held it in the staging queue until its TTL expired.
// DelayAMQPMessage 是 handler 看到的一次投递。broker 已经把它在暂存队列里留到 TTL 到期。
type DelayAMQPMessage struct {
	Envelope
	// Attempt is 1 on first delivery and 2 once the broker marks it redelivered.
	// Attempt 第一次投递为 1，broker 标记重投后为 2。
	Attempt int32
	// Delivery is the native delivery, including the x-death header the broker
	// adds on dead-lettering and the producer's Timestamp.
	// Delivery 是原生投递，包含 broker 死信时附加的 x-death 头与生产者写入的 Timestamp。
	Delivery amqp091.Delivery
}

// DelayAMQPHandlerFunc processes one delayed AMQP delivery.
// DelayAMQPHandlerFunc 处理一次延迟 AMQP 投递。
type DelayAMQPHandlerFunc func(ctx context.Context, cfg DelayAMQPConfig, msg DelayAMQPMessage) error

// DelayAMQPConsumer is one delayed subscription, ready for a worker group.
// DelayAMQPConsumer 是一个延迟订阅，可以直接放进 worker 分组。
type DelayAMQPConsumer struct {
	cfg  DelayAMQPConfig
	conn gofyamqp.Config
}

// NewDelayAMQP validates cfg, resolves its connection block and returns the
// consumer. It does not connect; Run does.
// NewDelayAMQP 校验 cfg、解析连接段并返回消费者。它不建连，建连在 Run 里。
func NewDelayAMQP(conns Connections, cfg DelayAMQPConfig) (*DelayAMQPConsumer, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("delay amqp consumer %q: %w", cfg.Name, err)
	}
	conn, err := conns.ResolveAMQP(cfg.Instance)
	if err != nil {
		return nil, fmt.Errorf("delay amqp consumer %q: %w", cfg.Name, err)
	}
	return &DelayAMQPConsumer{cfg: cfg, conn: conn}, nil
}

func (c *DelayAMQPConsumer) Name() string { return c.cfg.Name }

func (c *DelayAMQPConsumer) Concurrency() uint8 { return concurrency(c.cfg.Concurrency) }

// QueueConfig builds the transport-level configuration for this subscription.
// A producer should publish through it, so the staging and target topologies
// cannot drift apart between the two sides of the loop.
// QueueConfig 生成该订阅的传输层配置。生产端应通过它发布，这样闭环两侧的暂存拓扑与
// 目标拓扑就不会各自演化而错位。
func (c DelayAMQPConfig) QueueConfig() gofyamqp.QueueConfig {
	durable := !c.NonDurable
	return gofyamqp.QueueConfig{
		Topology: gofyamqp.Topology{
			Exchange:     c.DelayExchange,
			ExchangeType: c.DelayExchangeType,
			Queue:        c.DelayQueue,
			BindingKey:   c.DelayBindingKey,
			RoutingKey:   c.DelayRoutingKey,
			Durable:      durable,
		},
		Delay: &gofyamqp.DelayConfig{
			TTL:                c.TTL,
			PreserveRoutingKey: c.PreserveRoutingKey,
			Target: gofyamqp.Topology{
				Exchange:     c.Exchange,
				ExchangeType: c.ExchangeType,
				Queue:        c.Queue,
				BindingKey:   c.BindingKey,
				RoutingKey:   c.RoutingKey,
				Durable:      durable,
			},
		},
	}
}

// Run connects, declares both halves of the topology, consumes the target
// queue until ctx is cancelled and releases the connection on the way out.
// Run 建连、声明两半拓扑、消费目标队列直到 ctx 取消，并在退出时释放连接。
func (c *DelayAMQPConsumer) Run(ctx context.Context) error {
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

	queue, err := gofyamqp.NewQueue(client, c.cfg.QueueConfig())
	if err != nil {
		return fmt.Errorf("%s: declare delay queue %s: %w", name, c.cfg.DelayQueue, err)
	}
	defer func() { _ = queue.Close() }()

	w, err := amqpconsumer.New(name, queue, amqpconsumer.Config{
		Handler: func(ctx context.Context, delivery amqp091.Delivery) error {
			return proc.Handle(ctx, delayAMQPMessage(delivery))
		},
		Options: gofyamqp.ConsumeOptions{
			PrefetchCount:  c.cfg.PrefetchCount,
			AutoAck:        c.cfg.AutoAck,
			DiscardOnError: c.cfg.DiscardOnError,
			ReconnectDelay: c.cfg.ReconnectDelay,
			OnError: func(err error) {
				lg.Error("delay amqp consume error", zap.Error(err))
			},
		},
	})
	if err != nil {
		return err
	}

	lg.Info("delay amqp consumer started",
		zap.String("delay_queue", c.cfg.DelayQueue),
		zap.String("target_queue", c.cfg.Queue),
		zap.Duration("ttl", c.cfg.TTL),
		zap.Int("prefetch", c.cfg.PrefetchCount),
		zap.Bool("idempotency", c.cfg.Idempotency),
	)
	defer lg.Info("delay amqp consumer stopped", zap.String("target_queue", c.cfg.Queue))

	return w.Run(ctx)
}

func (c *DelayAMQPConsumer) handle(ctx context.Context, message Message, envelope Envelope) error {
	delivery, _ := message.Native.(amqp091.Delivery)
	return c.cfg.Handler(ctx, c.cfg, DelayAMQPMessage{
		Envelope: envelope,
		Attempt:  message.Attempt,
		Delivery: delivery,
	})
}

func (c DelayAMQPConfig) withDefaults() DelayAMQPConfig {
	if c.Instance == "" {
		c.Instance = "default"
	}
	if c.Concurrency == 0 {
		c.Concurrency = 1
	}
	if c.DelayExchangeType == "" {
		c.DelayExchangeType = "direct"
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

func (c DelayAMQPConfig) validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("name is required")
	}
	if !validName(c.Name) {
		return fmt.Errorf("invalid worker name %q", c.Name)
	}
	if c.Handler == nil {
		return errors.New("handler is required")
	}
	if err := validateConcurrency(c.Concurrency); err != nil {
		return err
	}
	if c.TTL < time.Millisecond {
		return errors.New("ttl must be at least 1ms")
	}
	if c.PrefetchCount < 0 {
		return errors.New("prefetch_count must not be negative")
	}
	if err := validateDelayAMQPTopology("delay", gofyamqp.Topology{
		Exchange:     c.DelayExchange,
		ExchangeType: c.DelayExchangeType,
		Queue:        c.DelayQueue,
		BindingKey:   c.DelayBindingKey,
		RoutingKey:   c.DelayRoutingKey,
	}); err != nil {
		return err
	}
	return validateDelayAMQPTopology("target", gofyamqp.Topology{
		Exchange:     c.Exchange,
		ExchangeType: c.ExchangeType,
		Queue:        c.Queue,
		BindingKey:   c.BindingKey,
		RoutingKey:   c.RoutingKey,
	})
}

// validateDelayAMQPTopology mirrors the checks the transport will run in
// NewQueue, so a bad declaration fails at registration instead of after a
// goroutine has already started.
// validateDelayAMQPTopology 复刻传输层在 NewQueue 里会做的检查，让错误的声明在
// 注册阶段就失败，而不是等 goroutine 起来之后。
func validateDelayAMQPTopology(label string, t gofyamqp.Topology) error {
	if strings.TrimSpace(t.Exchange) == "" {
		return fmt.Errorf("%s exchange is required", label)
	}
	if strings.TrimSpace(t.Queue) == "" {
		return fmt.Errorf("%s queue is required", label)
	}
	kind := strings.ToLower(strings.TrimSpace(t.ExchangeType))
	switch kind {
	case "", gofyamqp.ExchangeDirect, gofyamqp.ExchangeFanout,
		gofyamqp.ExchangeTopic, gofyamqp.ExchangeHeaders:
	default:
		return fmt.Errorf("%s exchange_type must be direct, fanout, topic or headers, got %q", label, t.ExchangeType)
	}
	if kind == "" || kind == gofyamqp.ExchangeDirect {
		routingKey, bindingKey := strings.TrimSpace(t.RoutingKey), strings.TrimSpace(t.BindingKey)
		if routingKey == "" && bindingKey == "" {
			return fmt.Errorf("%s direct exchange requires routing_key or binding_key", label)
		}
		if routingKey != "" && bindingKey != "" && routingKey != bindingKey {
			return fmt.Errorf("%s direct exchange requires routing_key == binding_key", label)
		}
	}
	// A topic exchange with an empty binding key only matches an empty routing
	// key, so the queue would sit there forever and never receive a message.
	// topic exchange 的 binding key 为空时只会匹配空 routing key，队列会一直空着，
	// 一条消息也收不到。
	if kind == gofyamqp.ExchangeTopic &&
		strings.TrimSpace(t.RoutingKey) == "" && strings.TrimSpace(t.BindingKey) == "" {
		return fmt.Errorf("%s topic exchange requires routing_key or binding_key", label)
	}
	return nil
}

// delayAMQPMessage reuses the immediate subscription's conversion, only
// changing the source so logs and traces tell a delayed delivery from a direct
// one.
// delayAMQPMessage 复用立即订阅的转换，只改 source，让日志与 trace 能区分延迟投递
// 与直接投递。
func delayAMQPMessage(delivery amqp091.Delivery) Message {
	out := amqpMessage(delivery)
	out.Source = "amqp_delay"
	return out
}
