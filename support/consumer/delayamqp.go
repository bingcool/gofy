package consumer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	gofyamqp "github.com/bingcool/gofy/support/amqp"
	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/bingcool/gofy/support/worker/consumer/amqpconsumer"
)

// DelayAMQPConfig describes one RabbitMQ delayed subscription.
// A delayed Queue has two topologies instead of one. The Delay* fields name
// the staging exchange/queue that holds a message until it expires; Exchange,
// Queue and their keys name the target that receives it once the broker
// dead-letters it. The consumer reads from the target — the staging queue has
// no consumer at all.
// Leave TTL at zero for the recommended publish-time-only mode: no
// x-message-ttl is declared and the producer decides every delay through
// amqp.PublishOptions.Delay, which becomes mandatory. A positive TTL keeps the
// classic fixed-delay queue, acting as both the default delay and an upper
// bound. Keep one delay per staging queue: the broker only checks the head of
// a queue for expiry, so mixing delays lets a long wait hold up shorter ones.
// Set Passive when another process (for example a Java service) already owns
// the declarations: both halves are then checked for existence instead of
// redeclared, so their broker-side arguments cannot collide with ours.
// DelayAMQPConfig 描述一个 RabbitMQ 延迟订阅。
// 延迟队列有两套拓扑而不是一套。Delay* 字段是消息等待过期的暂存 exchange/queue；
// Exchange、Queue 及其 key 是 broker 死信转发后的目标。消费者读的是目标队列，暂存
// 队列上完全没有消费者。
// TTL 留零即推荐的纯发布端模式：不声明 x-message-ttl，每条消息等多久由生产端通过
// amqp.PublishOptions.Delay 决定，该字段因此变成必填。TTL 为正时保留经典的固定延迟
// 队列，既是默认延迟也是上限。一个暂存队列只放一种延迟：broker 只在队头判定过期，
// 混用延迟会让前面的长等待挡住后面的短等待。
// 若拓扑由其它进程（例如 Java 服务）声明，请设置 Passive：两半拓扑只做存在性检查，
// 不重新声明，避免 broker 上已有的参数与本侧默认值冲突。
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
	// TTL is the optional queue-level TTL. Zero (the default) means the
	// publish-time-only mode: no x-message-ttl is declared and the producer
	// must set amqp.PublishOptions.Delay on every message. A positive value
	// must be at least 1ms and becomes both the default delay and the upper
	// bound for per-message ones.
	// TTL 是可选的队列级 TTL。零值（默认）为纯发布端模式：不声明 x-message-ttl，
	// 生产端必须给每条消息设置 amqp.PublishOptions.Delay。正值至少 1ms，既是默认
	// 延迟，也是单条消息延迟的上限。
	TTL time.Duration
	// PreserveRoutingKey keeps each message's original routing key when the
	// broker dead-letters it, instead of forcing one fixed key. Required for a
	// topic target: the routing key is what selects the binding, and a fixed
	// value collapses topic routing (or misses the binding and drops the message).
	// PreserveRoutingKey 让 broker 死信时保留每条消息原始的 routing key，而不是强制改成
	// 一个固定 key。目标是 topic exchange 时必须开启：绑定正是由 routing key 选中的，
	// 固定值会让 topic 分发失效，甚至匹配不上绑定而丢消息。
	PreserveRoutingKey bool
	// Passive checks that both halves of the topology already exist instead of
	// declaring them. Use it when another process (often written in another
	// language) owns the declarations: a normal QueueDeclare compares every
	// argument with the broker, so an x-message-ttl or dead-letter argument set
	// on their side but absent here fails with PRECONDITION_FAILED.
	//
	// Only the existence check changes. The staging and target names, keys and
	// TTL still drive publishing, binding and the pre-publish delay checks, so
	// they must match what the other process declared. In particular the broker
	// dead-letters with the arguments on its own queue, not with the
	// DelayConfig built here; a Java-side x-message-ttl also caps the wait at
	// its value. A missing exchange or queue fails with NOT_FOUND instead of
	// being created, which is the point: a foreign topology should be explicit.
	// Passive 让两半拓扑只做存在性检查，不再声明。当拓扑由其它进程（常是其它语言写的
	// 服务）创建时使用：普通 QueueDeclare 会逐个比对参数，对方设过 x-message-ttl 或死信
	// 参数而本侧没写，就会 PRECONDITION_FAILED。
	//
	// 变的只是检查方式。暂存/目标的名称、key 与 TTL 仍用于发布、绑定和发布前的延迟校验，
	// 因此必须与对方声明的一致。尤其要注意：broker 死信时用的是它自己队列上的参数，
	// 不是本侧构造的 DelayConfig；Java 侧若写了 x-message-ttl，等待时长也会被它截断。
	// exchange/queue 缺失时返回 NOT_FOUND 而不是自动创建——跨语言拓扑本就该显式对齐。
	Passive bool
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
	// noIDOnce keeps the "this queue cannot deduplicate" warning to one line per
	// consumer instead of one per message.
	// noIDOnce 让"该队列无法去重"的警告每个消费者只打一行，而不是每条消息一行。
	noIDOnce sync.Once
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

// ReportsReady tells the runner that Run reports readiness itself: both halves
// of the delayed topology are declared inside Run, so entering Run proves
// nothing about whether the subscription exists.
// ReportsReady 告诉 runner：Run 自行上报就绪。延迟拓扑的两半都在 Run 内声明，进入 Run
// 并不能证明订阅已经存在。
func (c *DelayAMQPConsumer) ReportsReady() {}

// RequiresDefaultDB reports the ledger's dependency on db.default: with
// idempotency on, this consumer cannot run without the framework-managed pool.
// RequiresDefaultDB 报告台账对 db.default 的依赖：幂等开启时，该消费者离不开框架
// 托管的连接池。
func (c *DelayAMQPConsumer) RequiresDefaultDB() bool { return c.cfg.Idempotency }

// QueueConfig builds the transport-level configuration for this subscription.
// A producer should publish through it, so the staging and target topologies
// cannot drift apart between the two sides of the loop. Passive is part of that
// contract: when the declarations belong to another process, neither side may
// quietly switch back to creating them.
// QueueConfig 生成该订阅的传输层配置。生产端应通过它发布，这样闭环两侧的暂存拓扑与
// 目标拓扑就不会各自演化而错位。Passive 也属于这份契约：拓扑归其它进程时，两侧都不能
// 悄悄变回自行声明。
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
			Passive:      c.Passive,
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
				Passive:      c.Passive,
			},
		},
	}
}

// Run connects, declares (or, with Passive, checks) both halves of the
// topology, consumes the target queue until ctx is cancelled and releases the
// connection on the way out.
// Run 建连、声明（Passive 时只检查）两半拓扑、消费目标队列直到 ctx 取消，并在退出时
// 释放连接。
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
			msg := delayAMQPMessage(delivery)
			if c.cfg.Idempotency && strings.TrimSpace(msg.ID) == "" {
				// Delayed deliveries keep the producer's MessageId through the
				// dead-letter hop. Without one the processor invents a random id
				// per delivery, so a redelivery can never be recognized as a
				// duplicate; say so once per subscription.
				// 延迟投递经过死信转发仍会保留生产端的 MessageId。没有它时处理器会给
				// 每次投递编随机 ID，重投永远认不出重复；这里按订阅只提醒一次。
				c.noIDOnce.Do(func() {
					lg.Warn("delay amqp deliveries without MessageId cannot be deduplicated",
						zap.String("queue", c.cfg.Queue),
					)
				})
			}
			return proc.Handle(ctx, msg)
		},
		Options: gofyamqp.ConsumeOptions{
			PrefetchCount:  c.cfg.PrefetchCount,
			AutoAck:        c.cfg.AutoAck,
			DiscardOnError: c.cfg.DiscardOnError,
			// Same in-flight rule as the immediate AMQP consumer.
			// 与立即 AMQP 消费者相同的在途规则。
			InFlightDelay:  DefaultProcessingLease,
			ReconnectDelay: c.cfg.ReconnectDelay,
			OnError: func(err error) {
				lg.Error("delay amqp consume error", zap.Error(err))
			},
			// Both halves of the delayed topology must exist and the target
			// consumer must be registered before this subscription can accept
			// work.
			// 延迟拓扑的两半都必须已声明、目标队列的消费者已注册，这个订阅才能接活。
			OnReady: func() { gofyworker.Ready(ctx) },
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
	if c.TTL < 0 || (c.TTL > 0 && c.TTL < time.Millisecond) {
		return errors.New("ttl must be 0 or at least 1ms")
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
