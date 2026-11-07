package kafka

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

var (
	// ErrGroupRequired is returned when ConsumerConfig.Group is empty.
	ErrGroupRequired = errors.New("kafka: consumer group is required")
	// ErrInvalidOffsetReset is returned for an unknown auto_offset_reset.
	ErrInvalidOffsetReset = errors.New("kafka: auto_offset_reset must be earliest or latest")
	// ErrHandlerRequired is returned when Consume receives a nil handler.
	ErrHandlerRequired = errors.New("kafka: handler is required")
)

// ConsumerConfig describes one consumer group subscription.
//
// The group is part of the client configuration in franz-go, so NewConsumer
// builds a dedicated client for a managed Client. For a Client built by Wrap,
// these group settings are not applied: the injected client must already be
// configured with kgo.ConsumerGroup(Group).
type ConsumerConfig struct {
	Topic string `yaml:"topic"`
	Group string `yaml:"group"`
	// DisableAutoCommit switches to manual commits after each successfully
	// handled batch. Auto commit is on by default, matching the swoolefy
	// library; it commits polled offsets, so a failed record may still be
	// committed. Use manual commits when you need controlled at-least-once.
	DisableAutoCommit bool `yaml:"disable_auto_commit"`
	// AutoCommitInterval is the auto commit period. Zero uses 200ms.
	AutoCommitInterval time.Duration `yaml:"auto_commit_interval" default:"200ms"`
	// AutoOffsetReset is earliest or latest. Zero value uses earliest.
	AutoOffsetReset string `yaml:"auto_offset_reset" default:"earliest"`
	// SessionTimeout is the group session timeout. Zero uses 45s.
	SessionTimeout time.Duration `yaml:"session_timeout" default:"45s"`
	// OnPartitionsAssigned and OnPartitionsRevoked are rebalance hooks. They
	// only apply to consumers built from a managed Client; franz-go stores
	// them on the client, so injected clients must configure their own.
	OnPartitionsAssigned func(ctx context.Context, assigned map[string][]int32) `yaml:"-"`
	OnPartitionsRevoked  func(ctx context.Context, revoked map[string][]int32)  `yaml:"-"`
}

func (c ConsumerConfig) validate() error {
	if strings.TrimSpace(c.Topic) == "" {
		return ErrTopicRequired
	}
	if strings.TrimSpace(c.Group) == "" {
		return ErrGroupRequired
	}
	switch strings.ToLower(strings.TrimSpace(c.AutoOffsetReset)) {
	case "", "earliest", "latest":
	default:
		return ErrInvalidOffsetReset
	}
	if c.AutoCommitInterval < 0 || c.SessionTimeout < 0 {
		return errors.New("kafka: durations must not be negative")
	}
	return nil
}

func (c ConsumerConfig) withDefaults() ConsumerConfig {
	out := c
	out.Topic = strings.TrimSpace(out.Topic)
	out.Group = strings.TrimSpace(out.Group)
	out.AutoOffsetReset = strings.ToLower(strings.TrimSpace(out.AutoOffsetReset))
	if out.AutoOffsetReset == "" {
		out.AutoOffsetReset = "earliest"
	}
	if out.AutoCommitInterval == 0 {
		out.AutoCommitInterval = 200 * time.Millisecond
	}
	if out.SessionTimeout == 0 {
		out.SessionTimeout = 45 * time.Second
	}
	return out
}

func (c ConsumerConfig) clientOptions() []kgo.Opt {
	reset := kgo.NewOffset().AtStart()
	if c.AutoOffsetReset == "latest" {
		reset = kgo.NewOffset().AtEnd()
	}
	opts := []kgo.Opt{
		kgo.ConsumerGroup(c.Group),
		kgo.ConsumeTopics(c.Topic),
		kgo.ConsumeResetOffset(reset),
		kgo.SessionTimeout(c.SessionTimeout),
		kgo.AutoCommitInterval(c.AutoCommitInterval),
	}
	if c.DisableAutoCommit {
		opts = append(opts, kgo.DisableAutoCommit())
	}
	if c.OnPartitionsAssigned != nil {
		opts = append(opts, kgo.OnPartitionsAssigned(
			func(ctx context.Context, _ *kgo.Client, assigned map[string][]int32) {
				c.OnPartitionsAssigned(ctx, assigned)
			}))
	}
	if c.OnPartitionsRevoked != nil {
		opts = append(opts, kgo.OnPartitionsRevoked(
			func(ctx context.Context, _ *kgo.Client, revoked map[string][]int32) {
				c.OnPartitionsRevoked(ctx, revoked)
			}))
	}
	return opts
}

// HandlerFunc processes one record. Returning an error stops Consume unless
// ConsumeOptions.DiscardOnError is set.
type HandlerFunc func(ctx context.Context, record *kgo.Record) error

// ConsumeOptions controls one Consume call.
type ConsumeOptions struct {
	// DiscardOnError reports a handler failure and continues with the next
	// record, committing the failed offset when manual commits are enabled.
	// The default returns the error: the failed record and the records after
	// it are redelivered from the last committed offset.
	DiscardOnError bool
	// MaxRecords limits records returned by one poll; zero uses every buffered
	// record.
	MaxRecords int
	// RetryDelay pauses after a poll error that produced no records; zero uses
	// 1s. Poll errors that still returned records are reported without a pause.
	RetryDelay time.Duration
	// OnError receives fetch, handler, commit and panic errors. It is called
	// from the Consume goroutine.
	OnError func(error)
}

func (o ConsumeOptions) withDefaults() ConsumeOptions {
	if o.RetryDelay <= 0 {
		o.RetryDelay = time.Second
	}
	return o
}

// Consumer reads one topic as part of a consumer group. A Consumer is meant
// for one Consume goroutine.
type Consumer struct {
	raw    *kgo.Client
	cfg    ConsumerConfig
	owned  bool
	closed atomic.Bool
}

// NewConsumer validates cfg and subscribes to its topic.
//
// For a managed Client it builds a dedicated franz-go client from the shared
// Config plus cfg, and Close closes that client. For a wrapped Client it adds
// the topic to the injected client's subscription; the group, offset reset and
// rebalance hooks must already be part of that client's configuration.
func NewConsumer(client *Client, cfg ConsumerConfig) (*Consumer, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	raw, baseCfg, managed, err := client.rawClient()
	if err != nil {
		return nil, err
	}
	if !managed {
		raw.AddConsumeTopics(cfg.Topic)
		return &Consumer{raw: raw, cfg: cfg}, nil
	}

	opts := baseCfg.clientOptions()
	opts = append(opts, cfg.clientOptions()...)
	derived, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafka: new consumer client: %w", err)
	}
	return &Consumer{raw: derived, cfg: cfg, owned: true}, nil
}

// Config returns the effective consumer configuration.
func (c *Consumer) Config() ConsumerConfig {
	return c.cfg
}

// Consume polls records and calls handler until ctx is cancelled. A cancelled
// context returns nil, so Consume can be used directly as a goroutine body.
//
// The default error policy is at-least-once: a failed record is not committed,
// Consume returns the handler error, and a restart rejoins the group from the
// last commit. Set DiscardOnError to skip poison records instead. Auto commit
// is convenient but commits polled offsets; disable it for controlled offsets.
func (c *Consumer) Consume(ctx context.Context, handler HandlerFunc, opts ConsumeOptions) error {
	if handler == nil {
		return ErrHandlerRequired
	}
	if c == nil || c.raw == nil {
		return ErrClientRequired
	}
	if c.closed.Load() {
		return ErrClientClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	opts = opts.withDefaults()

	for {
		if ctx.Err() != nil {
			return nil
		}
		fetches := c.raw.PollRecords(ctx, opts.MaxRecords)
		if ctx.Err() != nil {
			return nil
		}
		if fetches.IsClientClosed() {
			return ErrClientClosed
		}

		errs := fetches.Errors()
		for _, fetchErr := range errs {
			report(opts, fmt.Errorf("kafka: fetch %s/%d: %w", fetchErr.Topic, fetchErr.Partition, fetchErr.Err))
		}
		if len(errs) > 0 && fetches.NumRecords() == 0 {
			if !wait(ctx, opts.RetryDelay) {
				return nil
			}
			continue
		}
		if err := c.process(ctx, fetches, handler, opts); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

// Close releases the dedicated client of a managed consumer. A consumer built
// from a wrapped client only stops this Consumer and leaves the shared client
// open.
func (c *Consumer) Close() error {
	if c == nil {
		return nil
	}
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	if c.owned && c.raw != nil {
		c.raw.Close()
	}
	return nil
}

func (c *Consumer) process(ctx context.Context, fetches kgo.Fetches, handler HandlerFunc, opts ConsumeOptions) error {
	var handlerErr error
	fetches.EachPartition(func(partition kgo.FetchTopicPartition) {
		if handlerErr != nil {
			return
		}
		var last *kgo.Record
		for _, record := range partition.Records {
			if ctx.Err() != nil {
				return
			}
			err := callHandler(ctx, handler, record)
			if err == nil {
				last = record
				continue
			}
			report(opts, fmt.Errorf("kafka: handle %s/%d@%d: %w",
				record.Topic, record.Partition, record.Offset, err))
			if opts.DiscardOnError {
				// 丢弃失败消息时一并提交位点，避免每次启动都重试同一条毒消息。
				last = record
				continue
			}
			// 默认语义：先提交此前成功的位点，再停止消费。失败消息及后续消息
			// 会在下次启动或重平衡后重新投递。
			c.commit(ctx, last, opts)
			handlerErr = err
			return
		}
		c.commit(ctx, last, opts)
	})
	return handlerErr
}

func (c *Consumer) commit(ctx context.Context, record *kgo.Record, opts ConsumeOptions) {
	if record == nil || !c.cfg.DisableAutoCommit || ctx.Err() != nil {
		return
	}
	if err := c.raw.CommitRecords(ctx, record); err != nil {
		report(opts, fmt.Errorf("kafka: commit %s/%d@%d: %w",
			record.Topic, record.Partition, record.Offset, err))
	}
}

func callHandler(ctx context.Context, handler HandlerFunc, record *kgo.Record) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("kafka: handler panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return handler(ctx, record)
}

func report(opts ConsumeOptions, err error) {
	if opts.OnError != nil {
		opts.OnError(err)
	}
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
