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
	ErrDeliveryClosed = errors.New("amqp: delivery channel closed")
)

// Queue declares one topology and publishes or consumes through it.
//
// Publishing uses a shared channel guarded by a mutex. Consuming opens a
// dedicated channel per Consume call, so a long-running consumer never blocks
// publishers and multiple consumers can run on the same Queue.
type Queue struct {
	client *Client
	cfg    QueueConfig

	mu sync.Mutex
	ch *amqp091.Channel
}

// NewQueue validates cfg and binds it to client.
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
func (q *Queue) Config() QueueConfig {
	return q.cfg
}

// PublishOptions overrides the defaults for one message.
type PublishOptions struct {
	// RoutingKey overrides QueueConfig.RoutingKey. Topic publishers use it to
	// address a specific binding.
	RoutingKey  string
	ContentType string
	// Persistent sets delivery mode 2 so the message survives a broker restart
	// when its queue is durable. The default is transient.
	Persistent bool
	Priority   uint8
	Headers    amqp091.Table
	// Expiration is the per-message TTL. The broker applies the smaller of this
	// and the queue-level TTL.
	Expiration    time.Duration
	MessageID     string
	CorrelationID string
	ReplyTo       string
	Type          string
	AppID         string
	Timestamp     time.Time
	// Mandatory is forwarded to the broker. basic.return notifications are not
	// consumed by this package.
	Mandatory bool
}

// Publish sends body to the queue's exchange. With Confirm enabled it waits
// for the broker to accept the message and returns an error on nack.
func (q *Queue) Publish(ctx context.Context, body []byte, opts PublishOptions) error {
	if ctx == nil {
		ctx = context.Background()
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
	if opts.Expiration > 0 {
		publishing.Expiration = fmt.Sprintf("%d", opts.Expiration.Milliseconds())
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
type HandlerFunc func(ctx context.Context, msg amqp091.Delivery) error

// ConsumeOptions controls one consumer.
type ConsumeOptions struct {
	// ConsumerTag is optional; the broker generates one when empty.
	ConsumerTag string
	// AutoAck disables manual acknowledgements. The handler result is then
	// ignored for ack/nack purposes.
	AutoAck   bool
	Exclusive bool
	NoLocal   bool
	NoWait    bool
	// PrefetchCount limits unacknowledged deliveries; zero means 1.
	PrefetchCount int
	PrefetchSize  int
	Global        bool
	// DiscardOnError nacks failed messages without requeue, which drops them or
	// routes them to a dead-letter exchange. The default requeues.
	DiscardOnError bool
	// ReconnectDelay is the pause before rebuilding a lost consumer; zero means
	// 3s.
	ReconnectDelay time.Duration
	Arguments      amqp091.Table
	// OnError receives handler failures, ack/nack failures and reconnect
	// errors. It is called from the consumer goroutine.
	OnError func(error)
}

// Consume declares the topology and processes deliveries until ctx is
// cancelled. Handler errors, ack failures and lost connections are reported
// through Options.OnError; the consumer then reconnects after
// ConsumeOptions.ReconnectDelay.
//
// A cancelled context returns nil, so Consume can be used directly as a
// goroutine body with a long-lived application context.
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
