package amqp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

var (
	ErrClientRequired  = errors.New("amqp: client is required")
	ErrHandlerRequired = errors.New("amqp: handler is required")
	// ErrDeliveryClosed means the broker cancelled the consumer or the channel
	// went away; Consume reconnects unless the context was cancelled.
	// ErrDeliveryClosed 表示 broker 取消了消费者或 channel 已失效；除非上下文
	// 被取消，Consume 会重连。
	ErrDeliveryClosed = errors.New("amqp: delivery channel closed")
	// ErrInvalidDelay is a per-message delay the staging queue cannot honour.
	// ErrInvalidDelay 表示暂存队列无法满足的单条消息延迟。
	ErrInvalidDelay = errors.New("amqp: invalid message delay")
	// ErrDelayRequiresWorkflow guards against a message expiring on a plain
	// queue with nowhere to be dead-lettered: it would simply disappear.
	// ErrDelayRequiresWorkflow 防止消息在普通队列上到期：没有死信目的地时它会
	// 直接消失。
	ErrDelayRequiresWorkflow = errors.New("amqp: per-message delay requires QueueConfig.Delay")
)

// Queue declares one topology and publishes or consumes through it.
//
// Publishing uses a shared channel guarded by a mutex. Consuming opens a
// dedicated channel per Consume call, so a long-running consumer never blocks
// publishers and multiple consumers can run on the same Queue.
// Queue 声明一套 topology，并通过它发布或消费。
// 发布使用由互斥锁保护的共享 channel；消费为每次 Consume 打开独立 channel，
// 因此长跑的消费者不会阻塞发布者，同一个 Queue 上也能并行多个消费者。
type Queue struct {
	client *Client
	cfg    QueueConfig

	mu sync.Mutex
	ch *amqp091.Channel
}

// NewQueue validates cfg and binds it to client.
// NewQueue 校验 cfg 并把它绑定到 client。
func NewQueue(client *Client, cfg QueueConfig) (*Queue, error) {
	if client == nil {
		return nil, ErrClientRequired
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Queue{client: client, cfg: cfg}, nil
}

// Config returns the effective queue configuration.
// Config 返回实际生效的队列配置。
func (q *Queue) Config() QueueConfig {
	return q.cfg
}

// PublishOptions overrides the defaults for one message.
// PublishOptions 覆盖单条消息的默认值。
type PublishOptions struct {
	// RoutingKey overrides QueueConfig.RoutingKey. Topic publishers use it to
	// address a specific binding.
	// RoutingKey 覆盖 QueueConfig.RoutingKey。topic 发布者用它指向具体的绑定。
	RoutingKey  string
	ContentType string
	// Persistent sets delivery mode 2 so the message survives a broker restart
	// when its queue is durable. The default is transient.
	// Persistent 设置投递模式 2；队列为 durable 时消息可跨 broker 重启存活。
	// 默认是 transient。
	Persistent bool
	Priority   uint8
	Headers    amqp091.Table
	// Expiration is the per-message TTL. The broker applies the smaller of this
	// and the queue-level TTL.
	// Expiration 是单条消息的 TTL。broker 取它与队列级 TTL 中较小的那个。
	Expiration time.Duration
	// Delay is the relative form of Expiration for a delayed Queue: publish
	// now, deliver after this long. It becomes the message TTL, and the staging
	// queue dead-letters the message into DelayConfig.Target when it expires.
	// It must be at least 1ms and must not exceed DelayConfig.TTL, which is the
	// upper bound the broker would silently clamp to. Setting both Delay and
	// Expiration is an error; on a Queue without Delay it is an error too,
	// because an expiring message there has nowhere to go.
	// Delay 是 Expiration 在延迟队列上的相对写法：现在发布，经过这段时间后投递。
	// 它会被转成消息 TTL，暂存队列在消息到期时把它死信到 DelayConfig.Target。
	// 最小 1ms，且不能超过 DelayConfig.TTL（超过会被 broker 静默截断为 TTL）。
	// Delay 与 Expiration 同时设置会报错；普通队列上设置同样报错——那条消息到期后
	// 没有死信目的地，会直接消失。
	Delay         time.Duration
	MessageID     string
	CorrelationID string
	ReplyTo       string
	Type          string
	AppID         string
	Timestamp     time.Time
	// Mandatory is forwarded to the broker. basic.return notifications are not
	// consumed by this package.
	// Mandatory 原样传给 broker。本包不消费 basic.return 通知，未路由的消息
	// 不会被感知。
	Mandatory bool
}

// Publish sends body to the queue's exchange. With Confirm enabled it waits
// for the broker to accept the message and returns an error on nack.
// Publish 把 body 发到队列所属的 exchange。Confirm 打开时会等待 broker 确认，
// 收到 nack 则返回错误。
func (q *Queue) Publish(ctx context.Context, body []byte, opts PublishOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	expiration, err := messageExpiration(q.cfg, opts)
	if err != nil {
		return err
	}

	routingKey := opts.RoutingKey
	if strings.TrimSpace(routingKey) == "" {
		routingKey = q.cfg.routingKey()
	}
	publishing := amqp091.Publishing{
		Headers:       opts.Headers,
		ContentType:   opts.ContentType,
		DeliveryMode:  1,
		Priority:      opts.Priority,
		CorrelationId: opts.CorrelationID,
		ReplyTo:       opts.ReplyTo,
		MessageId:     opts.MessageID,
		Timestamp:     opts.Timestamp,
		Type:          opts.Type,
		AppId:         opts.AppID,
		Body:          body,
	}
	if opts.Persistent {
		publishing.DeliveryMode = 2
	}
	if expiration > 0 {
		publishing.Expiration = fmt.Sprintf("%d", expiration.Milliseconds())
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	ch, err := q.channel()
	if err != nil {
		return err
	}
	if q.cfg.Confirm {
		confirmation, err := ch.PublishWithDeferredConfirmWithContext(ctx, q.cfg.Exchange, routingKey, opts.Mandatory, false, publishing)
		if err != nil {
			q.resetChannel()
			return fmt.Errorf("amqp: publish: %w", err)
		}
		acked, err := confirmation.WaitContext(ctx)
		if err != nil {
			q.resetChannel()
			return fmt.Errorf("amqp: wait for publisher confirm: %w", err)
		}
		if !acked {
			return fmt.Errorf("amqp: broker rejected message on exchange %q", q.cfg.Exchange)
		}
		return nil
	}

	if err := ch.PublishWithContext(ctx, q.cfg.Exchange, routingKey, opts.Mandatory, false, publishing); err != nil {
		q.resetChannel()
		return fmt.Errorf("amqp: publish: %w", err)
	}
	return nil
}

// PublishJSON marshals value as JSON and publishes it. ContentType defaults to
// application/json.
// PublishJSON 把 value 序列化为 JSON 后发布，ContentType 默认 application/json。
func (q *Queue) PublishJSON(ctx context.Context, value any, opts PublishOptions) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("amqp: marshal message: %w", err)
	}
	if strings.TrimSpace(opts.ContentType) == "" {
		opts.ContentType = "application/json"
	}
	return q.Publish(ctx, body, opts)
}

// HandlerFunc processes one delivery. Returning nil acks the message; returning
// an error nacks it (requeue by default, see ConsumeOptions.DiscardOnError).
// HandlerFunc 处理一条投递。返回 nil 表示 ack；返回错误表示 nack（默认重新
// 入队，见 ConsumeOptions.DiscardOnError）。handler 内的 panic 会被捕获并
// 当作错误上报，消费者协程不会因此退出。
type HandlerFunc func(ctx context.Context, msg amqp091.Delivery) error

// ConsumeOptions controls one consumer.
// ConsumeOptions 控制单个消费者。
type ConsumeOptions struct {
	// ConsumerTag is optional; the broker generates one when empty.
	// ConsumerTag 可选，为空时由 broker 生成。
	ConsumerTag string
	// AutoAck disables manual acknowledgements. The handler result is then
	// ignored for ack/nack purposes.
	// AutoAck 关闭手动确认，此时 handler 的结果不再影响 ack/nack。
	AutoAck   bool
	Exclusive bool
	NoLocal   bool
	NoWait    bool
	// PrefetchCount limits unacknowledged deliveries; zero means 1.
	// PrefetchCount 限制未确认的投递数，0 表示 1。
	PrefetchCount int
	PrefetchSize  int
	Global        bool
	// DiscardOnError nacks failed messages without requeue, which drops them or
	// routes them to a dead-letter exchange. The default requeues.
	// DiscardOnError 让失败消息以不重新入队的方式 nack，即丢弃或转入死信
	// exchange。默认重新入队。
	DiscardOnError bool
	// ReconnectDelay is the pause before rebuilding a lost consumer; zero means
	// 3s.
	// ReconnectDelay 是重建丢失的消费者前的等待时间，0 表示 3s。
	ReconnectDelay time.Duration
	Arguments      amqp091.Table
	// OnError receives handler failures, ack/nack failures and reconnect
	// errors. It is called from the consumer goroutine.
	// OnError 接收 handler 失败、ack/nack 失败与重连错误。回调在消费者
	// goroutine 中执行，不应阻塞。
	OnError func(error)
}

// Consume declares the topology and processes deliveries until ctx is
// cancelled. Handler errors, ack failures and lost connections are reported
// through Options.OnError; the consumer then reconnects after
// ConsumeOptions.ReconnectDelay.
//
// A cancelled context returns nil, so Consume can be used directly as a
// goroutine body with a long-lived application context.
// Consume 声明 topology 并持续处理投递，直到 ctx 被取消。handler 错误、
// ack 失败与连接丢失经 Options.OnError 上报，之后按
// ConsumeOptions.ReconnectDelay 重连。
// ctx 被取消时返回 nil，因此 Consume 可以直接作为 goroutine 主体，配合长生命
// 周期的应用上下文使用。
func (q *Queue) Consume(ctx context.Context, handler HandlerFunc, opts ConsumeOptions) error {
	if handler == nil {
		return ErrHandlerRequired
	}
	if strings.TrimSpace(q.consumeQueue()) == "" {
		return ErrQueueRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	opts = opts.withDefaults()

	for {
		if ctx.Err() != nil {
			return nil
		}
		err := q.consumeOnce(ctx, handler, opts)
		if ctx.Err() != nil {
			return nil
		}
		if opts.OnError != nil && err != nil {
			opts.OnError(err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(opts.ReconnectDelay):
		}
	}
}

// Close releases the publishing channel. A later Publish reopens it; Consume
// opens its own channels and is unaffected.
// Close 释放发布用的 channel。之后的 Publish 会重新打开；Consume 使用自己的
// channel，不受影响。
func (q *Queue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.ch == nil {
		return nil
	}
	err := q.ch.Close()
	q.ch = nil
	if err != nil && !errors.Is(err, amqp091.ErrClosed) {
		return fmt.Errorf("amqp: close channel: %w", err)
	}
	return nil
}

func (q *Queue) consumeQueue() string {
	if q.cfg.Delay != nil {
		return q.cfg.Delay.Target.Queue
	}
	return q.cfg.Queue
}

func (q *Queue) consumeOnce(ctx context.Context, handler HandlerFunc, opts ConsumeOptions) error {
	ch, err := q.client.Channel()
	if err != nil {
		return err
	}
	defer func() { _ = ch.Close() }()

	if err := ch.Qos(opts.PrefetchCount, opts.PrefetchSize, opts.Global); err != nil {
		return fmt.Errorf("amqp: set qos: %w", err)
	}
	if err := q.declare(ch); err != nil {
		return err
	}
	queueName := q.consumeQueue()
	deliveries, err := ch.ConsumeWithContext(ctx, queueName, opts.ConsumerTag, opts.AutoAck, opts.Exclusive, opts.NoLocal, opts.NoWait, opts.Arguments)
	if err != nil {
		return fmt.Errorf("amqp: consume %q: %w", queueName, err)
	}

	for {
		select {
		case <-ctx.Done():
			// Drain deliveries before closing the channel. With unread buffered
			// messages the amqp091 consumer blocks the connection reader goroutine,
			// so the following Close never receives close-ok.
			// 排空投递再关 channel。amqp091 的消费者缓冲在有未读消息时会
			// 阻塞连接读取协程，导致随后的 Close 收不到 close-ok。
			for range deliveries {
			}
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				return ErrDeliveryClosed
			}
			q.handle(ctx, delivery, handler, opts)
		}
	}
}

func (q *Queue) handle(ctx context.Context, delivery amqp091.Delivery, handler HandlerFunc, opts ConsumeOptions) {
	err := callHandler(ctx, handler, delivery)
	if !opts.AutoAck {
		if err == nil {
			if ackErr := delivery.Ack(false); ackErr != nil {
				report(opts, fmt.Errorf("amqp: ack message: %w", ackErr))
			}
			return
		}
		if nackErr := delivery.Nack(false, !opts.DiscardOnError); nackErr != nil {
			report(opts, fmt.Errorf("amqp: nack message: %w", nackErr))
		}
	}
	if err != nil {
		report(opts, err)
	}
}

func callHandler(ctx context.Context, handler HandlerFunc, delivery amqp091.Delivery) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("amqp: handler panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return handler(ctx, delivery)
}

func report(opts ConsumeOptions, err error) {
	if opts.OnError != nil {
		opts.OnError(err)
	}
}

func (o ConsumeOptions) withDefaults() ConsumeOptions {
	if o.PrefetchCount == 0 {
		o.PrefetchCount = 1
	}
	if o.ReconnectDelay <= 0 {
		o.ReconnectDelay = 3 * time.Second
	}
	return o
}

func (q *Queue) channel() (*amqp091.Channel, error) {
	if q.ch != nil && !q.ch.IsClosed() {
		return q.ch, nil
	}
	ch, err := q.client.Channel()
	if err != nil {
		return nil, err
	}
	if q.cfg.Confirm {
		if err := ch.Confirm(false); err != nil {
			_ = ch.Close()
			return nil, fmt.Errorf("amqp: enable publisher confirms: %w", err)
		}
	}
	if err := q.declare(ch); err != nil {
		_ = ch.Close()
		return nil, err
	}
	q.ch = ch
	return ch, nil
}

func (q *Queue) declare(ch *amqp091.Channel) error {
	if q.cfg.Delay == nil {
		return q.cfg.Topology.declare(ch)
	}
	if err := q.cfg.Delay.declareTarget(ch); err != nil {
		return err
	}
	return q.cfg.declareDelaySource(ch)
}

func (q *Queue) resetChannel() {
	if q.ch == nil {
		return
	}
	_ = q.ch.Close()
	q.ch = nil
}
