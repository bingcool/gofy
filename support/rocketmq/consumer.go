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
	rmqv2 "github.com/apache/rocketmq-clients/golang/v5/protocol/v2"
)

const (
	defaultBatchSize         = 16
	defaultConcurrency       = 1
	defaultAwaitDuration     = 5 * time.Second
	defaultInvisibleDuration = 30 * time.Second
	// receiveBackoff slows the pull loop down after a non-timeout error
	// instead of hammering an unavailable proxy.
	// receiveBackoff 让拉取循环在非超时错误后退避，避免持续冲击不可用的 proxy。
	receiveBackoff = time.Second
)

var (
	// ErrGroupRequired is returned when ConsumerConfig.Group is empty.
	// ErrGroupRequired 在 ConsumerConfig.Group 为空时返回。
	ErrGroupRequired = errors.New("rocketmq: consumer group is required")
	// ErrTagsConflictSQL is returned when both SQL and a concrete tag filter
	// are configured.
	// ErrTagsConflictSQL 在同时配置 SQL 与具体 tag 过滤时返回。
	ErrTagsConflictSQL = errors.New("rocketmq: sql and tags cannot be set together")
	// ErrInvalidTag is returned for an empty tag or one containing "||".
	// ErrInvalidTag 在 tag 为空或含 "||" 时返回。
	ErrInvalidTag = errors.New("rocketmq: invalid tag")
	// ErrHandlerRequired is returned when Consume receives a nil handler.
	// ErrHandlerRequired 在 Consume 收到 nil handler 时返回。
	ErrHandlerRequired = errors.New("rocketmq: handler is required")
	// ErrConsumerStarted is returned when Consume is called twice on the same
	// Consumer.
	// ErrConsumerStarted 在同一个 Consumer 上重复调用 Consume 时返回。
	ErrConsumerStarted = errors.New("rocketmq: consumer already started")
)

// ConsumerConfig describes one pull-consumer group subscription.
//
// v5.1.2 has no push consumer: Consume long-polls with Receive and
// acknowledges each message. Ordering is preserved per queue within one worker
// batch; set Concurrency to 1 for one-at-a-time consumption.
// ConsumerConfig 描述一个 pull 消费者的组订阅。
// v5.1.2 没有 push 消费者：Consume 用 Receive 长轮询并逐条确认。单个 worker
// 批次内按队列保持顺序；需要严格串行消费时把 Concurrency 设为 1。
type ConsumerConfig struct {
	Topic string `yaml:"topic"`
	Group string `yaml:"group"`
	// Tags restricts the subscription to these tags. Empty or a single "*"
	// subscribes to every tag. Elements are joined with "||" and must not
	// contain that sequence themselves.
	// Tags 把订阅限定在这些 tag 上，为空或只有一个 "*" 时订阅全部 tag。多个元素
	// 用 "||" 连接，元素自身不能包含该串。
	Tags []string `yaml:"tags"`
	// SQL is an SQL92 property filter such as
	// "eventType IN ('created', 'cancelled')". It requires
	// enablePropertyFilter=true on the broker and cannot be combined with a
	// concrete Tags filter.
	// SQL 是 SQL92 属性过滤表达式，例如
	// "eventType IN ('created', 'cancelled')"。它要求 broker 打开
	// enablePropertyFilter=true，且不能与具体 Tags 过滤同时使用。
	SQL string `yaml:"sql"`
	// BatchSize is how many messages one Receive asks for. Zero uses 16.
	// BatchSize 是单次 Receive 请求的消息条数，0 表示 16。
	BatchSize int `yaml:"batch_size"`
	// Concurrency is the number of pull workers. Zero uses 1.
	// Concurrency 是拉取 worker 的数量，0 表示 1。
	Concurrency int `yaml:"concurrency"`
	// AwaitDuration is the broker long-poll wait of one Receive. Zero uses 5s.
	// AwaitDuration 是单次 Receive 在 broker 侧的长轮询等待时长，0 表示 5s。
	AwaitDuration time.Duration `yaml:"await_duration"`
	// InvisibleDuration hides a message from other consumers after delivery.
	// A failed handler leaves the message unacknowledged, so it is redelivered
	// after this duration. Zero uses 30s.
	// InvisibleDuration 是消息投递后对其他消费者的不可见时长。handler 失败时
	// 不 ack，消息在该时长结束后重投。0 表示 30s。
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
// Message 是一条收到的消息。Body 是原始负载，请用 encoding/json 或自己的编解码
// 器解析。
type Message struct {
	MessageID string
	Topic     string
	Tag       string
	Keys      []string
	Body      []byte
	// Properties are the user properties set by the producer.
	// Properties 是生产者设置的用户属性。
	Properties map[string]string
	// MessageGroup is set for FIFO messages.
	// MessageGroup 只在 FIFO 消息上设置。
	MessageGroup string
	// Offset is the queue offset reported by the broker.
	// Offset 是 broker 报告的队列位点。
	Offset int64
	// DeliveryAttempt starts at 1 and increases on redelivery.
	// DeliveryAttempt 从 1 开始，每次重投递增，可用于识别重复投递。
	DeliveryAttempt int32
	// DeliveryTimestamp is the scheduled delivery time of a delayed message.
	// DeliveryTimestamp 是延迟消息的计划投递时间。
	DeliveryTimestamp time.Time
	BornTimestamp     time.Time
}

// HandlerFunc processes one message. A nil return acknowledges it. A non-nil
// return leaves it unacknowledged, so the broker redelivers it after
// InvisibleDuration and DeliveryAttempt increases; make handlers idempotent by
// MessageID or Keys.
// HandlerFunc 处理一条消息。返回 nil 表示 ack；返回非 nil 则不 ack，broker 会在
// InvisibleDuration 之后重投并递增 DeliveryAttempt，因此 handler 必须按
// MessageID 或 Keys 实现幂等。handler 内的 panic 会被捕获并当作错误上报。
type HandlerFunc func(ctx context.Context, message *Message) error

// ConsumeOptions controls one Consume call.
// ConsumeOptions 控制单次 Consume 调用。
type ConsumeOptions struct {
	// DiscardOnError acknowledges a failed message and continues with the
	// rest of the batch. The default leaves it unacknowledged for redelivery.
	// DiscardOnError 对失败消息也 ack，并继续处理批次内其余消息。默认不 ack，
	// 留待重投。
	DiscardOnError bool
	// OnError receives handler, panic, receive and acknowledgement errors. It
	// is called from pull-worker goroutines.
	// OnError 接收 handler、panic、拉取与 ack 错误，回调在拉取 worker 的
	// goroutine 中执行，不应阻塞。
	OnError func(error)
	// OnReady runs once, after the SDK consumer started: the client is up, the
	// topic route resolved and the subscription registered with the broker.
	// Constructing the consumer only validates configuration, so this is the
	// first point at which the process can actually receive messages.
	//
	// It runs on the Consume goroutine, before the pull workers start.
	// OnReady 在 SDK 消费者启动成功后执行一次：客户端已启动、topic 路由已解析、订阅已
	// 注册到 broker。构造消费者只校验配置，因此这一刻才是进程真正能收消息的起点。
	//
	// 回调在 Consume 的 goroutine 中执行，早于拉取 worker 启动。
	OnReady func()
}

// Consumer reads one topic as part of a consumer group. RocketMQ consumers
// have their own client, so a Consumer is built from Config rather than from a
// Client. A Consumer is single-use: call Consume once, then Close.
// Consumer 以消费者组成员的身份读取一个 topic。RocketMQ 消费者自带客户端，
// 因此 Consumer 由 Config 而非 Client 构建。Consumer 是一次性对象：Consume
// 只能调用一次，之后调用 Close。
type Consumer struct {
	cfg ConsumerConfig
	// raw is the SDK SimpleConsumer behind an interface so the pull loop can
	// be tested without a broker.
	// raw 是被接口包住的 SDK SimpleConsumer，便于在没有 broker 时测试拉取循环。
	raw       consumerClient
	consuming atomic.Bool
	closed    atomic.Bool
	done      chan struct{}
	wg        sync.WaitGroup
	// mu serializes Start with Close so a close cannot race a starting
	// consumer.
	// mu 让 Start 与 Close 串行，避免关闭与启动并发竞争。
	mu sync.Mutex
}

// consumerClient is the subset of rmq.SimpleConsumer the pull loop uses.
// consumerClient 是拉取循环用到的 rmq.SimpleConsumer 子集。
type consumerClient interface {
	Start() error
	Stop() error
	Receive(ctx context.Context, maxMessageNum int32, invisibleDuration time.Duration) ([]deliveredMessage, error)
	Ack(ctx context.Context, delivery deliveredMessage) error
}

// deliveredMessage pairs the SDK view needed by Ack with the mapped message.
// deliveredMessage 把 Ack 需要的 SDK 视图与映射后的消息放在一起。
type deliveredMessage struct {
	view    *rmq.MessageView
	message *Message
}

// simpleConsumer adapts the SDK consumer and maps each view eagerly, so a
// handler never touches protocol types.
// simpleConsumer 适配 SDK 消费者，并立即完成视图映射，handler 因此不必接触协议
// 类型。
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
// NewConsumer 校验连接与订阅并构建 pull 消费者。连接要等到 Consume 被调用时才
// 建立。
func NewConsumer(cfg Config, consumerCfg ConsumerConfig) (*Consumer, error) {
	ensureSDKLogRoot()
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
// Config 返回实际生效的消费者配置。
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
// Consume 启动消费者并长轮询，直到 ctx 被取消或 Close 被调用。ctx 被取消时返回
// nil，因此可以直接作为 goroutine 主体使用。
// 一个 worker 按顺序处理批次内的每条消息：成功的 ack；失败的除非设置
// DiscardOnError，否则不 ack，并在 InvisibleDuration 后重投。Concurrency 大于 1
// 时批次由多个 worker 并行处理，批次之间不保证顺序。
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
	// Start is the subscription: the SDK resolved the topic route and registered
	// the consumer group. Reporting readiness here — instead of before Consume —
	// is what keeps a broker that rejects the subscription from looking healthy.
	// Start 就是订阅本身：SDK 解析了 topic 路由并注册了消费组。就绪在这里上报，而不是在
	// Consume 之前，订阅被 broker 拒绝时才不会看起来健康。
	if opts.OnReady != nil {
		opts.OnReady()
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

	// Stop the consumer only after the workers exit: Receive racing with
	// GracefulStop makes the SDK log meaningless receive errors.
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
// Close 通知 Consume 停止并立即返回，底层客户端由 Consume 在 worker 退出后关闭。
// 可重复调用，对从未启动的消费者也安全。
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
			if isIdleReceive(err) {
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
		// No ack: the broker redelivers after InvisibleDuration, so the handler
		// must be idempotent.
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

// isIdleReceive reports a long poll that came back empty. Two shapes mean it,
// and neither is a failure:
//
//   - the client side hits its own deadline; v5.1.2 returns a plain
//     fmt.Errorf("[error] CODE=DEADLINE_EXCEEDED") rather than a typed timeout,
//     so the string match is the only portable signal;
//   - the proxy answers instead, gRPC 40401 MESSAGE_NOT_FOUND with
//     "no new message". That is the server's long-poll window closing first,
//     and the SDK hands it back as *ErrRpcStatus, which AsErrRpcStatus unwraps.
//
// The second shape is why this is not just a timeout check: on an idle topic
// the proxy usually wins the race, so a caller that reported it would log an
// error every AwaitDuration, per worker, forever. Context cancellation stays
// the caller's business.
// isIdleReceive 判断一次长轮询是否空手而归。两种形态是同一件事，都不是失败：
//
//   - 客户端自己到期；v5.1.2 返回的是普通 fmt.Errorf("[error]
//     CODE=DEADLINE_EXCEEDED")，并非类型化超时，只能靠字符串匹配；
//   - 服务端先到期，proxy 直接回答 gRPC 40401 MESSAGE_NOT_FOUND 与
//     "no new message"。SDK 以 *ErrRpcStatus 返回，AsErrRpcStatus 会拆开包装。
//
// 后者正是这个判断不能只当成"超时"的原因：空 topic 上通常是 proxy 先答复，若当成
// 失败，每个 worker 每个 AwaitDuration 都会打一行错误，永不停止。上下文取消仍由
// 调用方判断。
func isIdleReceive(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if status, ok := rmq.AsErrRpcStatus(err); ok {
		return status.Code == int32(rmqv2.Code_MESSAGE_NOT_FOUND)
	}
	return strings.Contains(err.Error(), "CODE=DEADLINE_EXCEEDED")
}

// sleepCtx waits for d unless ctx ends first; it reports whether the timer
// fired.
// sleepCtx 等待 d，ctx 先结束则提前返回；返回值表示定时器是否到期。
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
// viewReader 是 receivedMessage 用到的 *rmq.MessageView 部分。该接口只为让字段
// 映射在没有 broker 时也能测试。
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
