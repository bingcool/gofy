package rocketmq

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	rmq "github.com/apache/rocketmq-clients/golang/v5"
)

const (
	defaultBatchSize         = 16
	defaultConcurrency       = 1
	defaultAwaitDuration     = 5 * time.Second
	defaultInvisibleDuration = 30 * time.Second
	// receiveBackoff slows the pull loop down after a non-timeout error
	// instead of hammering an unavailable proxy.
	receiveBackoff = time.Second
)

var (
	// ErrGroupRequired is returned when ConsumerConfig.Group is empty.
	ErrGroupRequired = errors.New("rocketmq: consumer group is required")
	// ErrTagsConflictSQL is returned when both SQL and a concrete tag filter
	// are configured.
	ErrTagsConflictSQL = errors.New("rocketmq: sql and tags cannot be set together")
	// ErrInvalidTag is returned for an empty tag or one containing "||".
	ErrInvalidTag = errors.New("rocketmq: invalid tag")
	// ErrHandlerRequired is returned when Consume receives a nil handler.
	ErrHandlerRequired = errors.New("rocketmq: handler is required")
	// ErrConsumerStarted is returned when Consume is called twice on the same
	// Consumer.
	ErrConsumerStarted = errors.New("rocketmq: consumer already started")
)

// ConsumerConfig describes one pull-consumer group subscription.
//
// v5.1.2 has no push consumer: Consume long-polls with Receive and
// acknowledges each message. Ordering is preserved per queue within one worker
// batch; set Concurrency to 1 for one-at-a-time consumption.
type ConsumerConfig struct {
	Topic string `yaml:"topic"`
	Group string `yaml:"group"`
	// Tags restricts the subscription to these tags. Empty or a single "*"
	// subscribes to every tag. Elements are joined with "||" and must not
	// contain that sequence themselves.
	Tags []string `yaml:"tags"`
	// SQL is an SQL92 property filter such as
	// "eventType IN ('created', 'cancelled')". It requires
	// enablePropertyFilter=true on the broker and cannot be combined with a
	// concrete Tags filter.
	SQL string `yaml:"sql"`
	// BatchSize is how many messages one Receive asks for. Zero uses 16.
	BatchSize int `yaml:"batch_size"`
	// Concurrency is the number of pull workers. Zero uses 1.
	Concurrency int `yaml:"concurrency"`
	// AwaitDuration is the broker long-poll wait of one Receive. Zero uses 5s.
	AwaitDuration time.Duration `yaml:"await_duration"`
	// InvisibleDuration hides a message from other consumers after delivery.
	// A failed handler leaves the message unacknowledged, so it is redelivered
	// after this duration. Zero uses 30s.
	InvisibleDuration time.Duration `yaml:"invisible_duration"`
}

func (c ConsumerConfig) validate() error {
	if strings.TrimSpace(c.Topic) == "" {
		return ErrTopicRequired
	}
	if strings.TrimSpace(c.Group) == "" {
		return ErrGroupRequired
	}
	if strings.TrimSpace(c.SQL) != "" && hasConcreteTags(c.Tags) {
		return ErrTagsConflictSQL
	}
	for _, tag := range c.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || strings.Contains(tag, "||") {
			return fmt.Errorf("%w: %q", ErrInvalidTag, tag)
		}
	}
	if c.BatchSize < 0 || c.Concurrency < 0 {
		return errors.New("rocketmq: batch_size and concurrency must not be negative")
	}
	if c.AwaitDuration < 0 || c.InvisibleDuration < 0 {
		return errors.New("rocketmq: await_duration and invisible_duration must not be negative")
	}
	return nil
}

func (c ConsumerConfig) withDefaults() ConsumerConfig {
	out := c
	out.Topic = strings.TrimSpace(out.Topic)
	out.Group = strings.TrimSpace(out.Group)
	out.SQL = strings.TrimSpace(out.SQL)
	tags := make([]string, len(c.Tags))
	for i, tag := range c.Tags {
		tags[i] = strings.TrimSpace(tag)
	}
	out.Tags = tags
	if out.BatchSize == 0 {
		out.BatchSize = defaultBatchSize
	}
	if out.Concurrency == 0 {
		out.Concurrency = defaultConcurrency
	}
	if out.AwaitDuration == 0 {
		out.AwaitDuration = defaultAwaitDuration
	}
	if out.InvisibleDuration == 0 {
		out.InvisibleDuration = defaultInvisibleDuration
	}
	return out
}

func (c ConsumerConfig) selector() *rmq.FilterExpression {
	expression, filterType := c.filter()
	return rmq.NewFilterExpressionWithType(expression, filterType)
}

func (c ConsumerConfig) filter() (string, rmq.FilterExpressionType) {
	if c.SQL != "" {
		return c.SQL, rmq.SQL92
	}
	return c.tagExpression(), rmq.TAG
}

func (c ConsumerConfig) tagExpression() string {
	if !hasConcreteTags(c.Tags) {
		return "*"
	}
	return strings.Join(c.Tags, " || ")
}

func hasConcreteTags(tags []string) bool {
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" && tag != "*" {
			return true
		}
	}
	return false
}

// Message is one received message. Body is the raw payload; use
// encoding/json or your own codec to decode it.
type Message struct {
	MessageID string
	Topic     string
	Tag       string
	Keys      []string
	Body      []byte
	// Properties are the user properties set by the producer.
	Properties map[string]string
	// MessageGroup is set for FIFO messages.
	MessageGroup string
	// Offset is the queue offset reported by the broker.
	Offset int64
	// DeliveryAttempt starts at 1 and increases on redelivery.
	DeliveryAttempt int32
	// DeliveryTimestamp is the scheduled delivery time of a delayed message.
	DeliveryTimestamp time.Time
	BornTimestamp     time.Time
}

// HandlerFunc processes one message. A nil return acknowledges it. A non-nil
// return leaves it unacknowledged, so the broker redelivers it after
// InvisibleDuration and DeliveryAttempt increases; make handlers idempotent by
// MessageID or Keys.
type HandlerFunc func(ctx context.Context, message *Message) error

// ConsumeOptions controls one Consume call.
type ConsumeOptions struct {
	// DiscardOnError acknowledges a failed message and continues with the
	// rest of the batch. The default leaves it unacknowledged for redelivery.
	DiscardOnError bool
	// OnError receives handler, panic, receive and acknowledgement errors. It
	// is called from pull-worker goroutines.
	OnError func(error)
}

// Consumer reads one topic as part of a consumer group. RocketMQ consumers
// have their own client, so a Consumer is built from Config rather than from a
// Client. A Consumer is single-use: call Consume once, then Close.
type Consumer struct {
	cfg ConsumerConfig
	// raw is the SDK SimpleConsumer behind an interface so the pull loop can
	// be tested without a broker.
	raw       consumerClient
	consuming atomic.Bool
	closed    atomic.Bool
	done      chan struct{}
	wg        sync.WaitGroup
	// mu serializes Start with Close so a close cannot race a starting
	// consumer.
	mu sync.Mutex
}

// consumerClient is the subset of rmq.SimpleConsumer the pull loop uses.
type consumerClient interface {
	Start() error
	Stop() error
	Receive(ctx context.Context, maxMessageNum int32, invisibleDuration time.Duration) ([]deliveredMessage, error)
	Ack(ctx context.Context, delivery deliveredMessage) error
}

// deliveredMessage pairs the SDK view needed by Ack with the mapped message.
type deliveredMessage struct {
	view    *rmq.MessageView
	message *Message
}

// simpleConsumer adapts the SDK consumer and maps each view eagerly, so a
// handler never touches protocol types.
type simpleConsumer struct {
	raw rmq.SimpleConsumer
}

func (c *simpleConsumer) Start() error {
	return c.raw.Start()
}

func (c *simpleConsumer) Stop() error {
	return c.raw.GracefulStop()
}

func (c *simpleConsumer) Receive(ctx context.Context, maxMessageNum int32, invisibleDuration time.Duration) ([]deliveredMessage, error) {
	views, err := c.raw.Receive(ctx, maxMessageNum, invisibleDuration)
	if err != nil {
		return nil, err
	}
	deliveries := make([]deliveredMessage, 0, len(views))
	for _, view := range views {
		if view == nil {
			continue
		}
		deliveries = append(deliveries, deliveredMessage{view: view, message: receivedMessage(view)})
	}
	return deliveries, nil
}

func (c *simpleConsumer) Ack(ctx context.Context, delivery deliveredMessage) error {
	return c.raw.Ack(ctx, delivery.view)
}

// NewConsumer validates the connection and subscription, and builds the pull
// consumer. It does not connect until Consume is called.
func NewConsumer(cfg Config, consumerCfg ConsumerConfig) (*Consumer, error) {
	if err := cfg.validateConnection(); err != nil {
		return nil, err
	}
	if err := consumerCfg.validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	consumerCfg = consumerCfg.withDefaults()

	raw, err := rmq.NewSimpleConsumer(cfg.rmqConfig(consumerCfg.Group), cfg.simpleConsumerOptions(consumerCfg)...)
	if err != nil {
		return nil, fmt.Errorf("rocketmq: new consumer %q: %w", consumerCfg.Group, err)
	}
	return &Consumer{
		cfg:  consumerCfg,
		raw:  &simpleConsumer{raw: raw},
		done: make(chan struct{}),
	}, nil
}

// Config returns the effective consumer configuration.
func (c *Consumer) Config() ConsumerConfig {
	if c == nil {
		return ConsumerConfig{}
	}
	out := c.cfg
	out.Tags = append([]string(nil), c.cfg.Tags...)
	return out
}

// Consume starts the consumer and long-polls until ctx is cancelled or Close
// is called. A cancelled context returns nil, so Consume can be used directly
// as a goroutine body.
//
// Every message in a batch is handled in order by one worker. A successfully
// handled message is acknowledged; a failed one is left unacknowledged and is
// redelivered after InvisibleDuration unless DiscardOnError is set. With
// Concurrency above 1, batches are processed by several workers and no order
// is guaranteed across them.
func (c *Consumer) Consume(ctx context.Context, handler HandlerFunc, opts ConsumeOptions) error {
	if c == nil || c.raw == nil {
		return ErrClientRequired
	}
	if handler == nil {
		return ErrHandlerRequired
	}
	if c.closed.Load() {
		return ErrClientClosed
	}
	if !c.consuming.CompareAndSwap(false, true) {
		return ErrConsumerStarted
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if err := c.start(); err != nil {
		return fmt.Errorf("rocketmq: start consumer %q: %w", c.cfg.Group, err)
	}

	consumeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	workers := c.cfg.Concurrency
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.poll(consumeCtx, handler, opts)
		}()
	}

	select {
	case <-ctx.Done():
	case <-c.done:
	}
	cancel()
	c.wg.Wait()

	// 工作协程退出后再停消费者：Receive 和 GracefulStop 并发会让 SDK
	// 记录无意义的接收错误。
	if err := c.raw.Stop(); err != nil && !c.closed.Load() && ctx.Err() == nil {
		report(opts, fmt.Errorf("rocketmq: shutdown consumer %q: %w", c.cfg.Group, err))
	}
	return nil
}

// Close asks Consume to stop and returns immediately. Consume stops the
// underlying client after its workers exit. It is safe to call more than once,
// and harmless on a consumer that never started.
func (c *Consumer) Close() error {
	if c == nil {
		return nil
	}
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(c.done)
	return nil
}

func (c *Consumer) start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed.Load() {
		return ErrClientClosed
	}
	return c.raw.Start()
}

func (c *Consumer) poll(ctx context.Context, handler HandlerFunc, opts ConsumeOptions) {
	for {
		if ctx.Err() != nil {
			return
		}
		deliveries, err := c.raw.Receive(ctx, int32(c.cfg.BatchSize), c.cfg.InvisibleDuration)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if isReceiveTimeout(err) {
				continue
			}
			report(opts, fmt.Errorf("rocketmq: receive %q: %w", c.cfg.Topic, err))
			if !sleepCtx(ctx, receiveBackoff) {
				return
			}
			continue
		}
		for _, delivery := range deliveries {
			if !c.process(ctx, delivery, handler, opts) {
				return
			}
		}
	}
}

func (c *Consumer) process(ctx context.Context, delivery deliveredMessage, handler HandlerFunc, opts ConsumeOptions) bool {
	if ctx.Err() != nil {
		return false
	}
	message := delivery.message
	err := callHandler(ctx, handler, message)
	switch {
	case err == nil:
		c.ack(ctx, delivery, "ack", opts)
	case opts.DiscardOnError:
		report(opts, fmt.Errorf("rocketmq: discard %s@%s: %w", message.Topic, message.MessageID, err))
		c.ack(ctx, delivery, "discard", opts)
	default:
		report(opts, fmt.Errorf("rocketmq: handle %s@%s: %w", message.Topic, message.MessageID, err))
		// 不 ack：InvisibleDuration 之后 broker 重投，handler 必须幂等。
	}
	return true
}

func (c *Consumer) ack(ctx context.Context, delivery deliveredMessage, action string, opts ConsumeOptions) {
	if err := c.raw.Ack(ctx, delivery); err != nil && ctx.Err() == nil {
		report(opts, fmt.Errorf("rocketmq: %s %s@%s: %w",
			action, delivery.message.Topic, delivery.message.MessageID, err))
	}
}

// isReceiveTimeout reports the SDK's long-poll timeout. v5.1.2 returns a plain
// fmt.Errorf("[error] CODE=DEADLINE_EXCEEDED") instead of a typed timeout, so
// the string match is the only portable signal; context cancellation is
// checked by the caller.
func isReceiveTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return strings.Contains(err.Error(), "CODE=DEADLINE_EXCEEDED")
}

// sleepCtx waits for d unless ctx ends first; it reports whether the timer
// fired.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// viewReader is the part of *rmq.MessageView receivedMessage reads. The
// interface exists only so field mapping is testable without a broker.
type viewReader interface {
	GetMessageId() string
	GetTopic() string
	GetTag() *string
	GetKeys() []string
	GetBody() []byte
	GetProperties() map[string]string
	GetMessageGroup() *string
	GetOffset() int64
	GetDeliveryAttempt() int32
	GetDeliveryTimestamp() *time.Time
	GetBornTimestamp() *time.Time
}

func receivedMessage(view viewReader) *Message {
	message := &Message{
		MessageID:       view.GetMessageId(),
		Topic:           view.GetTopic(),
		Keys:            append([]string(nil), view.GetKeys()...),
		Body:            view.GetBody(),
		Properties:      copyProperties(view.GetProperties()),
		Offset:          view.GetOffset(),
		DeliveryAttempt: view.GetDeliveryAttempt(),
	}
	if tag := view.GetTag(); tag != nil {
		message.Tag = *tag
	}
	if group := view.GetMessageGroup(); group != nil {
		message.MessageGroup = *group
	}
	if delivery := view.GetDeliveryTimestamp(); delivery != nil {
		message.DeliveryTimestamp = *delivery
	}
	if born := view.GetBornTimestamp(); born != nil {
		message.BornTimestamp = *born
	}
	return message
}

func copyProperties(properties map[string]string) map[string]string {
	if len(properties) == 0 {
		return nil
	}
	out := make(map[string]string, len(properties))
	for key, value := range properties {
		out[key] = value
	}
	return out
}

func callHandler(ctx context.Context, handler HandlerFunc, message *Message) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("rocketmq: handler panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return handler(ctx, message)
}

func report(opts ConsumeOptions, err error) {
	if opts.OnError != nil {
		opts.OnError(err)
	}
}
