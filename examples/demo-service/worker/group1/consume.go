package group1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/bingcool/gofy/core"
	gofyotel "github.com/bingcool/gofy/support/otel"
)

// Message is the protocol-independent view of one delivery. Each consumer
// adapter converts its own record type into this struct, so the handling chain
// below is written once instead of four times.
// Message 是一条投递与协议无关的视图。每个消费者适配器把自己的记录类型转成这个
// 结构，因此下面的处理链路只需要写一次，而不是四次。
type Message struct {
	// ID identifies the message across redeliveries. It is the idempotency key.
	// ID 在多次重投之间标识同一条消息，也是幂等键。
	ID string
	// Source is kafka, amqp, rocketmq or queues; it lands in logs and spans.
	// Source 取 kafka、amqp、rocketmq 或 queues，写进日志与 span。
	Source string
	// Topic is the Kafka topic, AMQP routing key, RocketMQ topic or Redis
	// queue key.
	// Topic 是 Kafka topic、AMQP routing key、RocketMQ topic 或 Redis 队列键。
	Topic string
	// Key is the partition key or message key when the protocol has one.
	// Key 是协议提供的分区键或消息键。
	Key string
	// Body is the raw payload.
	// Body 是原始消息体。
	Body []byte
	// Attempt counts deliveries, starting at 1.
	// Attempt 是投递次数，从 1 开始。
	Attempt int32
	// Headers carries the message attributes, including the W3C traceparent
	// used to continue the producer's trace.
	// Headers 携带消息属性，其中包含用于接续生产者链路的 W3C traceparent。
	Headers map[string]string
	// Enqueued is the broker timestamp when available.
	// Enqueued 是 broker 提供的时间戳，拿不到时为零值。
	Enqueued time.Time
}

// Envelope is the message contract shared by every producer and consumer in
// the demo. Keeping one shape means a new consumer only supplies a handler, not
// another decoder.
// Envelope 是示例中所有生产者与消费者共用的消息契约。统一形状意味着新增消费者
// 只需要提供 handler，不必再写一个解码器。
type Envelope struct {
	// ID is the event id; the consumer falls back to the transport message id.
	// ID 是事件 ID；消费者会回退到传输层的消息 ID。
	ID string `json:"id"`
	// Type selects the handler branch, for example order.cancelled.
	// Type 选择处理分支，例如 order.cancelled。
	Type string `json:"type"`
	// OccurredAt is when the producer emitted the event.
	// OccurredAt 是生产者发出事件的时间。
	OccurredAt time.Time `json:"occurred_at"`
	// Payload is the event body, decoded by the branch that handles Type.
	// Payload 是事件体，由处理 Type 的分支自行解码。
	Payload json.RawMessage `json:"payload"`
}

// HandlerFunc processes one decoded envelope.
//
// A nil return acknowledges the message. A permanent error is logged, parked as
// dead and acknowledged, because redelivering it cannot change the outcome. Any
// other error leaves the message unacknowledged so the broker redelivers it —
// which is also why handlers should be idempotent.
// HandlerFunc 处理一条已解码的信封。
//
// 返回 nil 表示确认消息。永久错误会记日志、标记为 dead 并确认，因为重投也改变
// 不了结果。其它错误则不确认，由 broker 重投——这也是 handler 必须幂等的原因。
type HandlerFunc func(ctx context.Context, envelope Envelope) error

// permanentError marks a failure that redelivery cannot fix: an unknown event
// type, a payload that does not decode, a required field that is missing.
// permanentError 标记重投也修不好的失败：未知事件类型、解不出的消息体、缺失的
// 必填字段。
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }

// Unwrap exposes the cause so errors.Is and errors.As keep working.
// Unwrap 暴露原因，使 errors.Is 与 errors.As 仍然可用。
func (e *permanentError) Unwrap() error { return e.err }

// Permanent wraps err as non-retryable. A nil err stays nil, so callers can
// write return Permanent(check()) without a nil guard.
// Permanent 把 err 标记为不可重试。err 为 nil 时结果仍为 nil，调用方因此可以直接
// 写 return Permanent(check())，不必先判空。
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err was wrapped by Permanent.
// IsPermanent 判断 err 是否由 Permanent 包装过。
func IsPermanent(err error) bool {
	var target *permanentError
	return errors.As(err, &target)
}

// DecodeEnvelope parses the shared message contract.
//
// Every failure here is permanent: the body will not become valid JSON on the
// fifth delivery. Returning the error lets the caller park the message instead
// of retrying it forever.
// DecodeEnvelope 解析共用的消息契约。
//
// 这里的所有失败都是永久性的：消息体不会在第五次投递时变成合法 JSON。返回错误
// 让调用方把它标记为 dead，而不是无限重试。
func DecodeEnvelope(msg Message) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(msg.Body, &env); err != nil {
		return Envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	env.Type = strings.TrimSpace(env.Type)
	if env.Type == "" {
		return Envelope{}, errors.New("envelope.type is required")
	}
	if strings.TrimSpace(env.ID) == "" {
		// The transport id is stable across redeliveries, so it is the right
		// fallback for the idempotency key.
		// 传输层 ID 在重投之间保持稳定，因此适合兜底作为幂等键。
		env.ID = msg.ID
	}
	if len(env.Payload) == 0 {
		env.Payload = json.RawMessage("{}")
	}
	return env, nil
}

// processorConfig is what a consumer passes to newProcessor.
// processorConfig 是消费者传给 newProcessor 的参数。
type processorConfig struct {
	// Consumer is the worker name; it scopes the idempotency rows.
	// Consumer 是 worker 名，用它划分幂等记录。
	Consumer string
	// Handler runs the business branch.
	// Handler 执行业务分支。
	Handler HandlerFunc
	// Idempotency enables the consumed-message table.
	// Idempotency 开启消费记录表。
	Idempotency bool
	// MaxAttempts bounds the retries of one message.
	// MaxAttempts 限制单条消息的重试次数。
	MaxAttempts int
}

// Processor is the shared middle of every consumer: it continues the trace,
// deduplicates, decodes, calls the handler and records the outcome.
//
// It is safe for concurrent use: every field is read-only after construction
// and per-message state lives on the stack.
// Processor 是每个消费者共用的中段：接续链路、去重、解码、调用 handler 并记录
// 结果。
//
// 可安全并发使用：构造之后所有字段只读，每条消息的状态都留在栈上。
type Processor struct {
	consumer    string
	handler     HandlerFunc
	deduper     *deduper
	logger      *zap.Logger
	tracer      trace.Tracer
	propagator  propagation.TextMapPropagator
	maxAttempts int
}

// newProcessor builds the shared chain. It fails when idempotency is requested
// without a database: silently dropping deduplication would turn every
// redelivery into a second side effect.
// newProcessor 构造共用链路。开启幂等却没有数据库时直接失败：静默放弃去重会让
// 每次重投都变成第二次副作用。
func newProcessor(rt *core.Runtime, cfg processorConfig) (*Processor, error) {
	if rt == nil {
		return nil, errors.New("consumer runtime is required")
	}
	if cfg.Handler == nil {
		return nil, errors.New("consumer handler is required")
	}
	p := &Processor{
		consumer:    cfg.Consumer,
		handler:     cfg.Handler,
		logger:      rt.Logger,
		tracer:      consumerTracer(rt),
		propagator:  rt.Propagator,
		maxAttempts: effectiveMaxAttempts(cfg.MaxAttempts),
	}
	if p.logger == nil {
		p.logger = zap.NewNop()
	}
	if p.propagator == nil {
		p.propagator = propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	}
	if cfg.Idempotency {
		if rt.DB == nil {
			return nil, fmt.Errorf("consumer %s: idempotency requires a database", cfg.Consumer)
		}
		p.deduper = &deduper{db: rt.DB, consumer: cfg.Consumer, maxAttempts: p.maxAttempts}
		if err := p.deduper.ensureSchema(context.Background()); err != nil {
			return nil, fmt.Errorf("consumer %s: idempotency schema: %w", cfg.Consumer, err)
		}
	}
	return p, nil
}

// Handle runs one message through the chain.
//
// The return value is the acknowledgement decision: nil acknowledges, a
// non-nil error asks for redelivery. Permanent failures and duplicates are
// acknowledged on purpose — retrying them only burns partitions.
// Handle 让一条消息走完链路。
//
// 返回值就是确认决定：nil 确认，非 nil 请求重投。永久失败与重复消息会被刻意确认
// ——重试它们只会白占分区。
func (p *Processor) Handle(ctx context.Context, msg Message) error {
	start := time.Now()
	if strings.TrimSpace(msg.ID) == "" {
		// A message without an id cannot be deduplicated; give it one so the
		// log line and the span still correlate.
		// 没有 ID 的消息无法去重，这里补一个，至少让日志与 span 能对上。
		msg.ID = uuid.NewString()
	}

	ctx = p.extract(ctx, msg.Headers)
	ctx, span := p.tracer.Start(ctx, "consume "+p.consumer,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", msg.Source),
			attribute.String("messaging.destination.name", msg.Topic),
			attribute.String("messaging.message.id", msg.ID),
			attribute.Int("messaging.message.delivery_attempt", int(msg.Attempt)),
		),
	)
	defer span.End()

	lg := gofyotel.Logger(ctx, p.logger).With(
		zap.String("consumer", p.consumer),
		zap.String("source", msg.Source),
		zap.String("topic", msg.Topic),
		zap.String("message_id", msg.ID),
		zap.Int32("attempt", msg.Attempt),
	)

	if p.deduper != nil {
		proceed, err := p.deduper.claim(ctx, msg.ID)
		if err != nil {
			// The deduplication store being down is not a reason to drop the
			// message: report it as retryable and let the broker redeliver.
			// 去重存储不可用不是丢消息的理由：按可重试错误上报，让 broker 重投。
			span.RecordError(err)
			span.SetStatus(codes.Error, "claim")
			lg.Error("consume claim failed", zap.Error(err), zap.Duration("duration", time.Since(start)))
			return fmt.Errorf("claim message %s: %w", msg.ID, err)
		}
		if !proceed {
			span.SetAttributes(attribute.Bool("messaging.duplicate", true))
			lg.Info("message skipped",
				zap.String("reason", "already processed or attempts exhausted"),
				zap.Duration("duration", time.Since(start)),
			)
			return nil
		}
	}

	env, err := DecodeEnvelope(msg)
	if err != nil {
		p.park(ctx, msg.ID, err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "decode")
		lg.Error("message discarded: undecodable envelope",
			zap.Error(err),
			zap.ByteString("body", truncateBytes(msg.Body, 256)),
			zap.Duration("duration", time.Since(start)),
		)
		return nil
	}

	err = p.handler(ctx, env)
	switch {
	case err == nil:
		if p.deduper != nil {
			if markErr := p.deduper.succeed(ctx, msg.ID); markErr != nil {
				// The side effect already happened, so the message is
				// acknowledged anyway; a failed marker only means the next
				// redelivery repeats work the handler must tolerate.
				// 副作用已经发生，消息照样确认；标记失败只意味着下次重投会重做，
				// 而 handler 本来就必须容忍这一点。
				lg.Warn("consume mark done failed", zap.Error(markErr))
			}
		}
		span.SetStatus(codes.Ok, "")
		lg.Info("message consumed",
			zap.String("event_id", env.ID),
			zap.String("event_type", env.Type),
			zap.Duration("duration", time.Since(start)),
		)
		return nil
	case IsPermanent(err):
		p.park(ctx, msg.ID, err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "permanent")
		lg.Error("message discarded: permanent failure",
			zap.String("event_id", env.ID),
			zap.String("event_type", env.Type),
			zap.Error(err),
			zap.Duration("duration", time.Since(start)),
		)
		return nil
	default:
		if p.deduper != nil {
			if markErr := p.deduper.fail(ctx, msg.ID, err); markErr != nil {
				lg.Warn("consume mark failed", zap.Error(markErr))
			}
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "retryable")
		lg.Warn("message handling failed, waiting for redelivery",
			zap.String("event_id", env.ID),
			zap.String("event_type", env.Type),
			zap.Error(err),
			zap.Duration("duration", time.Since(start)),
		)
		return err
	}
}

// extract continues the producer's trace when the message carries one.
// extract 在消息携带链路信息时接续生产者的调用链。
func (p *Processor) extract(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	return p.propagator.Extract(ctx, propagation.MapCarrier(headers))
}

// consumerTracer returns the runtime's tracer, falling back to a no-op one when
// a host built the runtime without a provider. The framework always supplies
// one; the fallback keeps a hand-built runtime from panicking at the first
// message instead of starting.
// consumerTracer 返回 Runtime 的 tracer；宿主构造 Runtime 时没有提供 provider
// 则回退到 no-op。框架总会提供，这个回退让手工构造的 Runtime 在第一条消息时不会
// panic。
func consumerTracer(rt *core.Runtime) trace.Tracer {
	if rt.TracerProvider == nil {
		return trace.NewNoopTracerProvider().Tracer("gofy/worker/consumer")
	}
	return rt.TracerProvider.Tracer("gofy/worker/consumer")
}

// park marks a message as dead so a later redelivery is skipped instead of
// repeating a failure that cannot succeed.
// park 把消息标记为 dead，使后续重投被跳过，而不是重复一次不可能成功的失败。
func (p *Processor) park(ctx context.Context, messageID string, cause error) {
	if p.deduper == nil {
		return
	}
	if err := p.deduper.park(ctx, messageID, cause); err != nil {
		// Failing to record the dead state must not hide the original error.
		// 记录 dead 失败不应该盖掉原始错误。
		p.logger.Warn("consume mark dead failed",
			zap.String("consumer", p.consumer),
			zap.String("message_id", messageID),
			zap.Error(err),
		)
	}
}

// Outcome values stored in worker_consumed_messages.status.
// worker_consumed_messages.status 中保存的结果值。
const (
	consumedStatusProcessing = "processing"
	consumedStatusDone       = "done"
	consumedStatusFailed     = "failed"
	consumedStatusDead       = "dead"
)

// consumedMessage is the idempotency ledger: one row per (consumer, message).
//
// The unique index is the whole mechanism. Claiming a message is an insert that
// either wins the row or learns it already exists; no lock, no read-modify-write
// window between two replicas.
// consumedMessage 是幂等台账：每个 (消费者, 消息) 一行。
//
// 唯一索引就是全部机制。认领一条消息是一次插入：要么拿到这行，要么得知它已存在；
// 没有锁，两个副本之间也没有读改写窗口。
type consumedMessage struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	Consumer  string    `gorm:"size:64;not null;uniqueIndex:uk_worker_consumed,priority:1"`
	MessageID string    `gorm:"size:128;not null;uniqueIndex:uk_worker_consumed,priority:2"`
	Status    string    `gorm:"size:16;not null;index:idx_worker_consumed_status"`
	Attempts  int       `gorm:"not null"`
	LastError string    `gorm:"size:512"`
	CreatedAt time.Time `gorm:"type:datetime(3)"`
	UpdatedAt time.Time `gorm:"type:datetime(3)"`
}

// TableName returns the ledger table name.
// TableName 返回台账表名。
func (consumedMessage) TableName() string { return "worker_consumed_messages" }

// deduper is the database-backed idempotency store.
// deduper 是基于数据库的幂等存储。
type deduper struct {
	db          *gorm.DB
	consumer    string
	maxAttempts int
}

// ensureSchema creates the ledger when it is missing.
//
// It checks first instead of calling AutoMigrate unconditionally: several
// consumer instances start at the same time, and two concurrent AutoMigrate
// calls both observe "table missing" and then race to create it — the loser
// gets error 1050 and, without this guard, would restart forever.
//
// migrations/0003_worker_consumed_messages.sql carries the same DDL for
// environments where schema changes go through a release process.
// ensureSchema 在台账缺失时创建它。
//
// 先检查再建，而不是无条件 AutoMigrate：多个消费者实例会同时启动，两个并发的
// AutoMigrate 都会看到"表不存在"然后抢着建——失败的那个拿到 1050 错误，没有这层
// 保护就会无限重启。
//
// migrations/0003_worker_consumed_messages.sql 提供同样的 DDL，供 schema 变更走
// 发布流程的环境使用。
func (d *deduper) ensureSchema(ctx context.Context) error {
	db := d.db.WithContext(ctx)
	if db.Migrator().HasTable(&consumedMessage{}) {
		return nil
	}
	if err := db.AutoMigrate(&consumedMessage{}); err != nil && !isTableExists(err) {
		return err
	}
	return nil
}

// isTableExists reports whether err is the "table already exists" race, which
// means another replica won and the schema is in place.
// isTableExists 判断 err 是不是"表已存在"的竞态结果——那说明另一个副本赢了，
// schema 已经就位。
func isTableExists(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}

// claim reserves the message for handling.
//
// It returns true when the caller should proceed: either the row was just
// created, or an earlier attempt failed and the budget is not exhausted. It
// returns false for a duplicate that already succeeded and for a message whose
// attempts ran out.
// claim 为处理预留这条消息。
//
// 返回 true 表示调用方应当继续：要么刚创建了行，要么此前失败但预算还没用完。
// 已成功处理过的重复消息、以及尝试次数耗尽的消息返回 false。
func (d *deduper) claim(ctx context.Context, messageID string) (bool, error) {
	row := consumedMessage{
		Consumer:  d.consumer,
		MessageID: messageID,
		Status:    consumedStatusProcessing,
		Attempts:  1,
	}
	// DoNothing turns the unique index into a cheap "was I first?" question
	// that also works on SQLite, so tests do not need MySQL-specific SQL.
	// DoNothing 把唯一索引变成一个廉价的"我是第一个吗"判断，在 SQLite 上也能
	// 用，测试因此不必写 MySQL 专有 SQL。
	tx := d.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if tx.Error != nil {
		return false, tx.Error
	}
	if tx.RowsAffected == 1 {
		return true, nil
	}

	var existing consumedMessage
	if err := d.db.WithContext(ctx).
		Where("consumer = ? AND message_id = ?", d.consumer, messageID).
		First(&existing).Error; err != nil {
		return false, err
	}
	if existing.Status == consumedStatusDone {
		return false, nil
	}
	if existing.Attempts >= d.maxAttempts {
		if existing.Status != consumedStatusDead {
			_ = d.db.WithContext(ctx).Model(&consumedMessage{}).
				Where("id = ? AND status <> ?", existing.ID, consumedStatusDone).
				Updates(map[string]any{
					"status":     consumedStatusDead,
					"last_error": "attempts exhausted",
				})
		}
		return false, nil
	}
	// The status guard makes two concurrent redeliveries race safely: only one
	// of them flips the row back to processing and increments the counter.
	// status 条件让两次并发重投安全竞争：只有其中一个能把行改回 processing 并
	// 递增计数。
	res := d.db.WithContext(ctx).Model(&consumedMessage{}).
		Where("id = ? AND status <> ?", existing.ID, consumedStatusDone).
		Updates(map[string]any{
			"status":   consumedStatusProcessing,
			"attempts": gorm.Expr("attempts + 1"),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// succeed records a completed message.
// succeed 记录一条已完成的消息。
func (d *deduper) succeed(ctx context.Context, messageID string) error {
	return d.db.WithContext(ctx).Model(&consumedMessage{}).
		Where("consumer = ? AND message_id = ?", d.consumer, messageID).
		Updates(map[string]any{"status": consumedStatusDone, "last_error": ""}).Error
}

// fail records a retryable failure so the next attempt sees the reason.
// fail 记录一次可重试失败，让下一次尝试能看到原因。
func (d *deduper) fail(ctx context.Context, messageID string, cause error) error {
	return d.db.WithContext(ctx).Model(&consumedMessage{}).
		Where("consumer = ? AND message_id = ?", d.consumer, messageID).
		Updates(map[string]any{
			"status":     consumedStatusFailed,
			"last_error": truncate(cause.Error(), 512),
		}).Error
}

// park records a failure that will not be retried.
// park 记录一次不再重试的失败。
func (d *deduper) park(ctx context.Context, messageID string, cause error) error {
	return d.db.WithContext(ctx).Model(&consumedMessage{}).
		Where("consumer = ? AND message_id = ?", d.consumer, messageID).
		Updates(map[string]any{
			"status":     consumedStatusDead,
			"last_error": truncate(cause.Error(), 512),
		}).Error
}

// truncate cuts s to at most n bytes without splitting a rune.
// truncate 把 s 截到最多 n 字节，且不切断多字节字符。
func truncate(s string, n int) string {
	return string(truncateBytes([]byte(s), n))
}

// truncateBytes cuts b to at most n bytes on a rune boundary.
// truncateBytes 在字符边界上把 b 截到最多 n 字节。
func truncateBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	cut := b[:n]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRune(cut)
		// A valid rune ends the slice; RuneError with size 1 means the cut
		// split a character, so drop one byte and look again.
		// 合法字符就是截断点；RuneError 且 size 为 1 说明切断了字符，去掉一个
		// 字节再看。
		if r != utf8.RuneError || size > 1 {
			return cut
		}
		cut = cut[:len(cut)-1]
	}
	return cut
}
