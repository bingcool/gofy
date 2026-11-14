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
	// ErrGroupRequired 在 ConsumerConfig.Group 为空时返回。
	ErrGroupRequired = errors.New("kafka: consumer group is required")
	// ErrInvalidOffsetReset is returned for an unknown auto_offset_reset.
	// ErrInvalidOffsetReset 在 auto_offset_reset 取值未知时返回。
	ErrInvalidOffsetReset = errors.New("kafka: auto_offset_reset must be earliest or latest")
	// ErrInvalidCommitMode is returned for an unknown commit_mode.
	// ErrInvalidCommitMode 在 commit_mode 取值未知时返回。
	ErrInvalidCommitMode = errors.New("kafka: commit_mode must be manual or auto")
	// ErrHandlerRequired is returned when Consume receives a nil handler.
	// ErrHandlerRequired 在 Consume 收到 nil handler 时返回。
	ErrHandlerRequired = errors.New("kafka: handler is required")
)

// CommitMode controls how a consumer commits offsets.
// CommitMode 控制消费者提交位点的方式。
type CommitMode string

const (
	// CommitModeManual commits only records the handler accepted. A failed
	// record is not committed, so it is redelivered after a restart, and
	// records after it wait until it succeeds or is discarded. This is the
	// default and the safe choice when losing a message is unacceptable.
	// CommitModeManual 只提交 handler 处理成功的记录。失败的记录不提交，重启后
	// 会重新投递；它之后的记录也会一起等待，直到该记录成功或被丢弃。这是默认
	// 值，也是不能丢消息时的安全选择。
	CommitModeManual CommitMode = "manual"
	// CommitModeAuto lets franz-go commit polled offsets every
	// AutoCommitInterval regardless of the handler result, so a failed record
	// may already be committed. Use it only for repeatable side effects such as
	// cache refreshes or config sync.
	// CommitModeAuto 让 franz-go 按 AutoCommitInterval 提交已拉取的位点，与
	// handler 结果无关，失败的记录可能已被提交。仅用于缓存刷新、配置同步等
	// 可重复执行的副作用。
	CommitModeAuto CommitMode = "auto"
)

// ConsumerConfig describes one consumer group subscription.
//
// The group is part of the client configuration in franz-go, so NewConsumer
// builds a dedicated client for a managed Client. For a Client built by Wrap,
// these group settings are not applied: the injected client must already be
// configured with kgo.ConsumerGroup(Group) and, for manual mode,
// kgo.DisableAutoCommit().
// ConsumerConfig 描述一个消费者组订阅。
// 组配置在 franz-go 中属于客户端配置，因此受管的 Client 会由 NewConsumer
// 另建专用客户端。Wrap 创建的 Client 不会应用这些组设置：注入的客户端必须
// 已经配置好 kgo.ConsumerGroup(Group)，手动提交模式还需
// kgo.DisableAutoCommit()。
type ConsumerConfig struct {
	Topic string `yaml:"topic"`
	Group string `yaml:"group"`
	// CommitMode selects manual (default) or auto offset commits. An empty
	// value means manual.
	// CommitMode 选择手动（默认）或自动提交位点，空值表示手动。
	CommitMode CommitMode `yaml:"commit_mode"`
	// DisableAutoCommit is deprecated: true is equivalent to
	// CommitModeManual. false no longer enables auto commits, because manual
	// is the default; set commit_mode: auto to opt in. When both fields are
	// set, CommitMode wins.
	// DisableAutoCommit 已废弃：true 等价于 CommitModeManual，false 不再表示
	// 开启自动提交，因为默认就是手动；如需自动提交请设 commit_mode: auto。
	// 两个字段同时设置时以 CommitMode 为准。
	DisableAutoCommit bool `yaml:"disable_auto_commit"`
	// AutoCommitInterval is the auto commit period. Zero uses 200ms.
	// AutoCommitInterval 是自动提交周期，0 表示 200ms。
	AutoCommitInterval time.Duration `yaml:"auto_commit_interval" default:"200ms"`
	// AutoOffsetReset is earliest or latest. Zero value uses earliest.
	// AutoOffsetReset 取 earliest 或 latest，零值表示 earliest。
	AutoOffsetReset string `yaml:"auto_offset_reset" default:"earliest"`
	// SessionTimeout is the group session timeout. Zero uses 45s.
	// SessionTimeout 是消费者组会话超时，0 表示 45s。
	SessionTimeout time.Duration `yaml:"session_timeout" default:"45s"`
	// BlockRebalanceOnPoll holds a rebalance until the polled batch has been
	// handled and committed, which is what franz-go offers in place of Java's
	// max.poll.interval.ms. That setting exists because a Java consumer stops
	// heartbeating while the application thread runs; franz-go heartbeats on
	// its own goroutine, so a slow handler never drops the member out of the
	// group. What can still happen without this flag is a rebalance landing
	// between the handler and the commit, which fails the commit and replays
	// the batch. With it, the batch finishes first.
	//
	// Off by default: a handler that never returns would then hold the group
	// hostage, and a stuck consumer is worse than a replay.
	// BlockRebalanceOnPoll 让重平衡等到这一批处理并提交完成，这是 franz-go 对
	// Java max.poll.interval.ms 的替代。后者存在的原因是 Java 消费者在应用线程
	// 执行期间停止心跳；franz-go 由独立 goroutine 心跳，因此慢 handler 不会把成员
	// 踢出消费组。不开这个开关仍可能发生的是：重平衡恰好落在 handler 与提交之间，
	// 导致提交失败、整批重放。打开它，这一批会先跑完。
	//
	// 默认关闭：永远不返回的 handler 会拖住整个组，卡死的消费者比重放更糟。
	BlockRebalanceOnPoll bool `yaml:"block_rebalance_on_poll"`
	// OnPartitionsAssigned and OnPartitionsRevoked are rebalance hooks. They
	// only apply to consumers built from a managed Client; franz-go stores
	// them on the client, so injected clients must configure their own.
	// OnPartitionsAssigned 与 OnPartitionsRevoked 是重平衡钩子，仅对受管
	// Client 创建的消费者生效；franz-go 把它们存在客户端上，注入的客户端需
	// 自行配置。
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
	switch CommitMode(strings.ToLower(strings.TrimSpace(string(c.CommitMode)))) {
	case "", CommitModeManual, CommitModeAuto:
	default:
		return ErrInvalidCommitMode
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
	out.CommitMode = CommitMode(strings.ToLower(strings.TrimSpace(string(out.CommitMode))))
	// DisableAutoCommit=true only keeps its old meaning; false must not turn
	// auto commits back on, otherwise existing configs could silently switch
	// back after this version changes the default.
	// DisableAutoCommit=true 只保留原语义；false 不能重新开启自动提交，否则
	// 本版本改动默认值后，存量配置会悄悄切回自动提交。
	if out.CommitMode == "" {
		out.CommitMode = CommitModeManual
	}
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
	if c.CommitMode != CommitModeAuto {
		opts = append(opts, kgo.DisableAutoCommit())
	}
	if c.BlockRebalanceOnPoll {
		opts = append(opts, kgo.BlockRebalanceOnPoll())
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
// HandlerFunc 处理一条记录。返回错误会终止 Consume，除非设置了
// ConsumeOptions.DiscardOnError。handler 内的 panic 会被捕获并当作错误上报。
type HandlerFunc func(ctx context.Context, record *kgo.Record) error

// ConsumeOptions controls one Consume call.
// ConsumeOptions 控制单次 Consume 调用。
type ConsumeOptions struct {
	// DiscardOnError reports a handler failure and continues with the next
	// record, committing the failed offset when manual commits are enabled.
	// The default returns the error: the failed record and the records after
	// it are redelivered from the last committed offset.
	// DiscardOnError 上报 handler 失败后继续处理下一条记录，手动提交模式下
	// 同时提交失败记录的位点。默认行为是返回错误：失败记录及其之后的记录会
	// 从最后提交的位点重新投递。
	DiscardOnError bool
	// MaxRecords limits records returned by one poll; zero uses every buffered
	// record.
	// MaxRecords 限制单次拉取返回的记录数，0 表示取回全部已缓冲记录。
	MaxRecords int
	// RetryDelay pauses after a poll error that produced no records; zero uses
	// 1s. Poll errors that still returned records are reported without a pause.
	// RetryDelay 是拉取出错且没有返回任何记录时的等待时间，0 表示 1s。仍返回
	// 了记录的拉取错误只上报，不暂停。
	RetryDelay time.Duration
	// OnError receives fetch, handler, commit and panic errors. It is called
	// from the Consume goroutine.
	// OnError 接收拉取、handler、提交与 panic 错误，回调在 Consume 的
	// goroutine 中执行，不应阻塞。
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
// Consumer 以消费者组成员的身份读取一个 topic。每个 Consumer 只对应一个
// Consume goroutine，不要并发调用 Consume。
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
// NewConsumer 校验 cfg 并订阅它的 topic。
// 受管 Client 会用共享 Config 叠加 cfg 另建专用 franz-go 客户端，Close 负责
// 关闭它。Wrap 创建的 Client 只把 topic 加到注入客户端的订阅上，组配置、
// offset 重置与重平衡钩子必须已在那个客户端上配好。
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
// Config 返回实际生效的消费者配置。
func (c *Consumer) Config() ConsumerConfig {
	return c.cfg
}

// Consume polls records and calls handler until ctx is cancelled. A cancelled
// context returns nil, so Consume can be used directly as a goroutine body.
//
// With the default CommitModeManual, delivery is at-least-once: a failed record
// is not committed, Consume returns the handler error, and a restart rejoins
// the group from the last commit. Set DiscardOnError to skip poison records
// instead. CommitModeAuto lets franz-go commit polled offsets regardless of the
// handler result and can drop a failed record.
// Consume 持续拉取记录并调用 handler，直到 ctx 被取消。ctx 被取消时返回 nil，
// 因此可以直接作为 goroutine 主体使用。
// 默认 CommitModeManual 下语义为 at-least-once：失败记录不提交，Consume 返回
// handler 错误，重启后从最后提交位点重新入组消费。需要跳过毒消息时设置
// DiscardOnError。CommitModeAuto 则由 franz-go 提交已拉取的位点，失败的记录
// 可能被丢弃。
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
			c.allowRebalance()
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
			c.allowRebalance()
			if !wait(ctx, opts.RetryDelay) {
				return nil
			}
			continue
		}
		// Release the gate after the batch is handled and its offsets are
		// committed, never before: that ordering is the whole point of
		// BlockRebalanceOnPoll.
		// 在这一批处理完、位点提交之后再放行，绝不提前——这个顺序正是
		// BlockRebalanceOnPoll 的意义。
		err := c.process(ctx, fetches, handler, opts)
		c.allowRebalance()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

// allowRebalance releases the gate that BlockRebalanceOnPoll closes around a
// polled batch. It is a no-op when the option is off, so the loop can call it
// unconditionally instead of duplicating every exit path.
// allowRebalance 放行 BlockRebalanceOnPoll 在一批记录上关掉的闸门。选项关闭时它是
// 空操作，因此循环可以无条件调用，不必给每条退出路径都复制一次。
func (c *Consumer) allowRebalance() {
	if c.cfg.BlockRebalanceOnPoll && c.raw != nil {
		c.raw.AllowRebalance()
	}
}

// Close releases the dedicated client of a managed consumer. A consumer built
// from a wrapped client only stops this Consumer and leaves the shared client
// open.
// Close 释放受管消费者自己的专用客户端。基于 Wrap 客户端构建的消费者只停止
// 自己，共享客户端保持打开。应先停 Consume 再调用 Close。
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
				// Committing the offset together with a discarded record keeps the
				// next start from retrying the same poison record.
				// 丢弃失败消息时一并提交位点，避免每次启动都重试同一条毒消息。
				last = record
				continue
			}
			// Default semantics: commit the offsets that already succeeded, then
			// stop. The failed record and the ones after it are redelivered on the
			// next start or rebalance.
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
	if record == nil || c.cfg.CommitMode == CommitModeAuto || ctx.Err() != nil {
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
