// Package consumer is the shared half of a resident consumer: the message
// envelope, a database-backed idempotency ledger, the permanent-versus-
// retryable split, and one constructor per broker.
//
// Each broker lives in its own file and is used the way a scan loop is: a config
// struct in the consumer's own file, a handler for the business branch, and
// nothing else. NewKafka, NewAMQP, NewRocketMQ and NewQueues each return a value
// that already satisfies worker.Worker — Name, Concurrency and Run — so a group
// registers it next to its polling loops without an adapter of its own.
//
// The chain takes a *core.Runtime rather than its pieces one by one, so a
// consumed message logs, traces and writes through exactly what the worker host
// built — not a second logger, tracer or pool.
// 包 consumer 是常驻消费者的共用部分：消息信封、基于数据库的幂等台账、"永久失败 /
// 可重试"的区分，以及每种 broker 一个构造器。
//
// 每种 broker 各占一个文件，用法与扫描循环一致：消费者自己的文件里一个配置结构、
// 一个业务 handler，没有别的。NewKafka、NewAMQP、NewRocketMQ、NewQueues 返回的值
// 本身就满足 worker.Worker（Name、Concurrency、Run），分组把它和轮询任务放在一起注册
// 即可，不必再写适配器。
//
// 链路接收 *core.Runtime 而不是逐个传入组件，是为了让消费到的消息用宿主构造的同一个
// logger、tracer 与连接池，而不是另起一套。
package consumer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/bingcool/gofy/support/headerpropagation"
	gofyotel "github.com/bingcool/gofy/support/otel"
	gofyworker "github.com/bingcool/gofy/support/worker"
)

// start builds what every consumer needs inside Run: the runtime's logger
// decorated with the worker identity, and the processing chain.
//
// Keeping it here rather than in each broker file is what makes the four
// constructors small enough to read in one sitting; the differences between
// them stay in connect-and-subscribe, which is the part worth reading.
// start 构造每个消费者在 Run 里都需要的东西：带上 worker 身份的 logger，以及处理链路。
//
// 放在这里而不是各 broker 文件里，是为了让四个构造器短到能一眼读完；它们之间的差异
// 留在"建连与订阅"那一段，那才是值得读的部分。
func start(ctx context.Context, name string, idempotency bool, maxAttempts int, handler HandlerFunc) (*Processor, *zap.Logger, error) {
	rt, ok := gofyworker.RuntimeFrom(ctx)
	if !ok {
		return nil, nil, fmt.Errorf("%s: worker runtime is missing from the context", name)
	}
	id, _ := gofyworker.IdentityFrom(ctx)
	logger := rt.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	lg := logger.With(
		zap.String("worker", id.String()),
		zap.String("consumer", name),
	)
	proc, err := NewProcessor(ctx, rt, ProcessorConfig{
		Consumer:    name,
		Handler:     handler,
		Idempotency: idempotency,
		MaxAttempts: maxAttempts,
	})
	if err != nil {
		return nil, nil, err
	}
	return proc, lg, nil
}

// concurrency converts a validated instance count to the worker API's type.
// Validation has already rejected anything outside 1..255, so the conversion
// cannot silently wrap.
// concurrency 把校验过的实例数转成 worker API 的类型。校验已经拒绝 1..255 之外的值，
// 因此转换不会悄悄回绕。
func concurrency(n int) uint8 {
	if n < 1 || n > 255 {
		return 1
	}
	return uint8(n)
}

// validateConcurrency rejects an instance count the worker API cannot hold. The
// upper bound is the worker API's: it takes a uint8, so 256 is a configuration
// mistake rather than a plan — and silently running one instance instead would
// hide it.
// validateConcurrency 拒绝 worker API 装不下的实例数。上限来自 worker API：它接收
// uint8，因此 256 是配置错误而不是计划——静默只跑一个实例会把这个错误藏起来。
func validateConcurrency(n int) error {
	if n < 1 || n > 255 {
		return fmt.Errorf("concurrency must be between 1 and 255, got %d", n)
	}
	return nil
}

// validName reports whether name can be used as a worker name. Consumers check
// it at registration so a mistake fails there instead of at group construction.
// validName 判断名字能否作为 worker 名。消费者在注册阶段检查，拼错的名字因此在那时
// 失败，而不是等到构造分组。
func validName(name string) bool { return gofyworker.ValidName(name) }

// The four consumers must stay registrable as workers without importing
// support/worker into their own files. These assertions are the compiler's
// copy of that promise: dropping Name or changing Run's signature breaks the
// build here instead of at a caller's group constructor.
// 四个消费者必须能在自己的文件不导入 support/worker 的前提下注册为 worker。下面这些
// 断言是编译器手里的那份承诺：少写 Name 或改了 Run 的签名，会在这里编译失败，而不是
// 在调用方的分组构造函数里。
var (
	_ gofyworker.Worker     = (*KafkaConsumer)(nil)
	_ gofyworker.Concurrent = (*KafkaConsumer)(nil)
	_ gofyworker.Worker     = (*AMQPConsumer)(nil)
	_ gofyworker.Concurrent = (*AMQPConsumer)(nil)
	_ gofyworker.Worker     = (*RocketMQConsumer)(nil)
	_ gofyworker.Concurrent = (*RocketMQConsumer)(nil)
	_ gofyworker.Worker     = (*QueuesConsumer)(nil)
	_ gofyworker.Concurrent = (*QueuesConsumer)(nil)
)

// The database requirement is the same kind of promise: every adapter has to
// expose its idempotency switch through DefaultDBRequirer, or a deployment
// without db.default would bind a consumer to a pool that is not there and only
// discover it inside Run.
// 数据库要求是同一份承诺：每个适配器都必须通过 DefaultDBRequirer 暴露自己的幂等开关，
// 否则没有 db.default 的部署会把消费者绑到一个不存在的池上，而且直到 Run 里才发现。
var (
	_ gofyworker.DefaultDBRequirer = (*KafkaConsumer)(nil)
	_ gofyworker.DefaultDBRequirer = (*AMQPConsumer)(nil)
	_ gofyworker.DefaultDBRequirer = (*RocketMQConsumer)(nil)
	_ gofyworker.DefaultDBRequirer = (*QueuesConsumer)(nil)
	_ gofyworker.DefaultDBRequirer = (*DelayAMQPConsumer)(nil)
	_ gofyworker.DefaultDBRequirer = (*DelayQueuesConsumer)(nil)
)

// Message is the protocol-independent view of one delivery. Each consumer
// adapter converts its own record type into this struct, so the handling chain
// below is written once instead of four times.
// Message 是一条投递与协议无关的视图。每个消费者适配器把自己的记录类型转成这个
// 结构，因此下面的处理链路只需要写一次，而不是四次。
type Message struct {
	// ID identifies the message across redeliveries. It is the idempotency key.
	// An id too long for the ledger column is stored there as a digest; the
	// handler and the log still see this value.
	// ID 在多次重投之间标识同一条消息，也是幂等键。超出台账列宽的 ID 在库里存成摘要，
	// handler 与日志看到的仍是这个值。
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
	// Native is the protocol record this message came from: a *kgo.Record, an
	// amqp091.Delivery, a *rocketmq.Message or a *queues.Message. The handler
	// receives it typed by its consumer, so this field exists only to carry it
	// through the shared chain.
	// Native 是这条消息来自的协议记录：*kgo.Record、amqp091.Delivery、
	// *rocketmq.Message 或 *queues.Message。handler 拿到的是自己那一种类型，这个字段
	// 只是把它带过共用链路。
	Native any
}

// Envelope is the message contract the chain decodes: an id, a type, the time
// the event occurred and a raw payload. Keeping one shape means adding a
// consumer is supplying a handler, not another decoder.
// Envelope 是链路解码的消息契约：ID、类型、事件发生时间与原始 payload。统一形状
// 意味着新增消费者只需要提供 handler，不必再写一个解码器。
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
// message carries the transport facts — attempt, headers and the native record —
// and envelope carries the decoded body. A nil return acknowledges the message.
// A permanent error is logged, parked as dead and acknowledged, because
// redelivering it cannot change the outcome. Any other error leaves the message
// unacknowledged so the broker redelivers it — which is also why handlers should
// be idempotent.
// HandlerFunc 处理一条已解码的信封。
//
// message 携带传输层事实——投递次数、消息头与原生记录，envelope 携带解码后的消息体。
// 返回 nil 表示确认消息。永久错误会记日志、标记为 dead 并确认，因为重投也改变不了
// 结果。其它错误则不确认，由 broker 重投——这也是 handler 必须幂等的原因。
type HandlerFunc func(ctx context.Context, message Message, envelope Envelope) error

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

// ProcessorConfig is what an adapter passes to NewProcessor.
// ProcessorConfig 是适配器传给 NewProcessor 的参数。
type ProcessorConfig struct {
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
	// ProcessingLease is how long a "processing" row may go without a renewal
	// before another delivery is allowed to take it over; zero means
	// DefaultProcessingLease. While the handler runs, the row is renewed every
	// third of this window under the attempts fence, so the lease no longer has
	// to cover the whole handler duration: it only has to outlive the gap
	// between two renewals plus one database write. A process that stalls for a
	// full window still loses the row to another replica, which is why handlers
	// must stay idempotent. Keeping it at least the broker's redelivery interval
	// avoids extra redelivery rounds on in-flight copies.
	// ProcessingLease 是一条 processing 记录在两次续期之间最多可保持多久、之后允许另一次
	// 投递接管；0 表示 DefaultProcessingLease。handler 运行期间，台账行每三分之一窗口在
	// attempts 围栏下续期一次，因此租约不再需要覆盖整个 handler 耗时：它只需活过两次续期之间
	// 的间隔加一次写库。进程整体停顿满一个窗口时行仍会被别的副本接管，所以 handler 必须幂等。
	// 取值不短于 broker 的重投间隔，可避免在途副本多转几轮重投。
	ProcessingLease time.Duration
	// SchemaTimeout bounds the ledger schema initialisation that runs when
	// Idempotency is on; zero means DefaultSchemaTimeout. The DDL itself is
	// cheap, but the connection it needs may not be: without a bound, a pool
	// that cannot reach the database keeps the consumer — and the worker
	// shutdown that is waiting for it — blocked for as long as the driver's own
	// timeout allows.
	// SchemaTimeout 限制开启 Idempotency 时执行的台账建表；0 表示 DefaultSchemaTimeout。
	// DDL 本身很轻，但它要的连接未必：没有上限时，连不上库的连接池会让消费者——以及等着
	// 它退出的 worker 关闭流程——一直阻塞到驱动自己的超时。
	SchemaTimeout time.Duration
	// OnLedgerError reports a ledger write that failed after the acknowledgement
	// decision was already made: a done/failed marker that could not be stored.
	// The message still follows that decision — a successful handler is
	// acknowledged even when its marker fails — so the ledger now lags reality:
	// the next redelivery of that message repeats the handler, and a "done" row
	// someone queries is missing. Wire this to a metric or an alert; without it
	// the only trace is a warning log line. It runs on the consumer goroutine
	// and must not block.
	// OnLedgerError 上报在确认决定已作出之后失败的台账写入，即没能落库的 done/failed
	// 标记。消息仍按该决定处理——handler 成功就会确认，即使标记写失败——因此台账从此落后
	// 于现实：该消息下次重投会重做 handler，别人查 "done" 也查不到这行。请把它接到指标或
	// 告警；不接则只剩一行 Warn 日志。它在消费者 goroutine 上执行，不得阻塞。
	OnLedgerError func(messageID string, err error)
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
	consumer      string
	handler       HandlerFunc
	deduper       *deduper
	logger        *zap.Logger
	tracer        trace.Tracer
	propagator    propagation.TextMapPropagator
	maxAttempts   int
	lease         time.Duration
	schemaTimeout time.Duration
	renewInterval time.Duration
	onLedgerError func(messageID string, err error)
}

// NewProcessor builds the shared chain. ctx is the consumer's lifecycle context
// and becomes the parent of the ledger schema initialisation, so cancelling it
// — on shutdown, or through SchemaTimeout — aborts the DDL instead of leaving
// the caller waiting on the database.
//
// It fails when idempotency is requested without db.default: silently dropping
// deduplication would turn every redelivery into a second side effect, and the
// ledger cannot live in a named instance.
//
// The ledger is process-wide and owns its schema, so it belongs to the
// framework-managed pool rather than to a business database that may be a read
// replica or a least-privilege account. The message names db.default on
// purpose: "requires a database" reads as if any configured instance would do,
// which is exactly the wrong conclusion for a deployment that only configures
// db.order. The worker host turns the same requirement into a startup error, so
// this branch is the defence for callers that build a processor directly.
// NewProcessor 构造共用链路。ctx 是消费者的生命周期 context，也是台账建表的父 context：
// 取消它——关闭流程，或 SchemaTimeout 到期——就会中止 DDL，而不是让调用方继续等数据库。
//
// 开启幂等却没有 db.default 时直接失败：静默放弃去重会让每次重投都变成第二次副作用，
// 而台账不能放在命名实例里。
//
// 台账是进程级的、自带 schema，因此属于框架托管的池，而不是业务库——后者可能是只读副本或
// 最小权限账号。文案刻意点名 db.default："requires a database" 读起来像"配了哪个实例都
// 行"，而对只配了 db.order 的部署来说，这恰恰是错误结论。worker 宿主会把同一个要求变成
// 启动期错误，因此这里只是给直接构造处理器的调用方兜底。
func NewProcessor(ctx context.Context, rt *core.Runtime, cfg ProcessorConfig) (*Processor, error) {
	if ctx == nil {
		return nil, errors.New("consumer context is required")
	}
	if rt == nil {
		return nil, errors.New("consumer runtime is required")
	}
	if cfg.Handler == nil {
		return nil, errors.New("consumer handler is required")
	}
	p := &Processor{
		consumer:      cfg.Consumer,
		handler:       cfg.Handler,
		logger:        rt.Logger,
		tracer:        consumerTracer(rt),
		propagator:    rt.Propagator,
		maxAttempts:   max(cfg.MaxAttempts, 1),
		lease:         cfg.ProcessingLease,
		schemaTimeout: cfg.SchemaTimeout,
		onLedgerError: cfg.OnLedgerError,
	}
	if p.lease <= 0 {
		p.lease = DefaultProcessingLease
	}
	if p.schemaTimeout <= 0 {
		p.schemaTimeout = DefaultSchemaTimeout
	}
	// Renew three times per lease window, so one slow database write cannot by
	// itself consume a whole window. The floor keeps a sub-millisecond lease
	// from turning into a tight renewal loop.
	// 每个租约窗口续期三次，一次慢写库不至于独自吃掉整个窗口。设下限是为了避免亚毫秒
	// 租约变成紧循环续期。
	p.renewInterval = p.lease / 3
	if p.renewInterval < 10*time.Millisecond {
		p.renewInterval = 10 * time.Millisecond
	}
	if p.logger == nil {
		p.logger = zap.NewNop()
	}
	if p.propagator == nil {
		p.propagator = propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	}
	if cfg.Idempotency {
		if rt.DB == nil {
			return nil, fmt.Errorf("consumer %s: idempotency requires db.default, the framework-managed database; named instances such as db.order cannot back the ledger", cfg.Consumer)
		}
		p.deduper = &deduper{
			db:          rt.DB,
			consumer:    cfg.Consumer,
			maxAttempts: p.maxAttempts,
			lease:       p.lease,
		}
		// The DDL runs under the caller's lifecycle context plus a bound of its
		// own. Both halves matter: cancellation makes a shutdown able to
		// interrupt a connection attempt, and the timeout keeps a consumer that
		// was built outside any lifecycle from waiting on the driver's timeout.
		// 建表跑在调用方的生命周期 context 之下，并额外加一层自己的上限。两者都必要：
		// 取消让关闭流程能打断建连，超时则避免在没有生命周期的构造路径上一直等到驱动的
		// 超时。
		schemaCtx, cancelSchema := context.WithTimeout(ctx, p.schemaTimeout)
		err := p.deduper.ensureSchema(schemaCtx)
		cancelSchema()
		if err != nil {
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
	// The ledger key is the id fitted to the column, kept apart from msg.ID so
	// the handler and the log keep the producer's value.
	// 台账键是压进列宽后的 ID，与 msg.ID 分开保存，让 handler 与日志保留生产者的原值。
	ledgerID := ledgerMessageID(msg.ID)

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
	if ledgerID != msg.ID {
		// The id was too long for the column and is stored as a digest; keep it
		// in the log so the row can still be found from the original id.
		// ID 超出列宽，存的是摘要；一并记进日志，才能从原始 ID 找到那一行。
		lg = lg.With(zap.String("ledger_id", ledgerID))
	}

	// The attempt number returned by claim is the fence for every write that
	// records this message's outcome. A redelivery that took the row over
	// carries a different number, so a slow handler finishing late cannot mark
	// the new owner's work done or failed.
	// claim 返回的 attempt 是记录本条消息结果时所有写回的围栏。接管了该行的重投
	// 持有不同的序号，因此迟到的旧 handler 既不能把新执行者的工作标成完成，也不能
	// 标成失败。
	attempt := 0
	if p.deduper != nil {
		c, err := p.deduper.claim(ctx, ledgerID)
		if err != nil {
			// The deduplication store being down is not a reason to drop the
			// message: report it as retryable and let the broker redeliver.
			// 去重存储不可用不是丢消息的理由：按可重试错误上报，让 broker 重投。
			span.RecordError(err)
			span.SetStatus(codes.Error, "claim")
			lg.Error("consume claim failed", zap.Error(err), zap.Duration("duration", time.Since(start)))
			return fmt.Errorf("claim message %s: %w", msg.ID, err)
		}
		if c.inFlight {
			// Another instance holds a valid processing lease. Acknowledge
			// nothing: the broker must redeliver later, otherwise a copy that
			// the first handler never finished would be gone for good.
			// 另一个实例持有有效的 processing 租约。此时不能确认消息：必须由 broker
			// 稍后重投，否则第一个 handler 没处理完的副本就永久消失了。
			span.SetAttributes(attribute.Bool("messaging.in_flight", true))
			span.SetStatus(codes.Error, "in flight")
			lg.Info("message still in flight on another instance, asking for redelivery",
				zap.Duration("duration", time.Since(start)),
			)
			return fmt.Errorf("message %s: %w", msg.ID, ErrInFlight)
		}
		if !c.proceed {
			span.SetAttributes(attribute.Bool("messaging.duplicate", true))
			lg.Info("message skipped",
				zap.String("reason", c.reason),
				zap.Duration("duration", time.Since(start)),
			)
			return nil
		}
		attempt = c.attempt
	}

	env, err := DecodeEnvelope(msg)
	if err != nil {
		// The dead state has to be durable before the broker drops the message:
		// an ACK without a ledger row leaves no trace of a message that never
		// succeeded. A failed write is therefore retryable, not a log line.
		// dead 状态必须先落库，broker 才能丢掉消息：没有台账行的 ACK 会让一条从未成功
		// 的消息不留任何痕迹。因此写失败是可重试错误，而不是一行日志。
		parkErr := p.park(ctx, ledgerID, attempt, err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "decode")
		lg.Error("message discarded: undecodable envelope",
			zap.Error(err),
			zap.ByteString("body", truncateBytes(msg.Body, 256)),
			zap.NamedError("park_error", parkErr),
			zap.Duration("duration", time.Since(start)),
		)
		return parkErr
	}

	err = p.runHandler(ctx, msg, env, attempt, ledgerID, lg)
	switch {
	case err == nil:
		if p.deduper != nil {
			if markErr := p.deduper.succeed(ctx, ledgerID, attempt); markErr != nil {
				// The side effect already happened, so the message is
				// acknowledged anyway; a failed marker only means the next
				// redelivery repeats work the handler must tolerate.
				// 副作用已经发生，消息照样确认；标记失败只意味着下次重投会重做，
				// 而 handler 本来就必须容忍这一点。
				p.ledgerFailed(lg, "done", ledgerID, markErr)
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
		parkErr := p.park(ctx, ledgerID, attempt, err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "permanent")
		lg.Error("message discarded: permanent failure",
			zap.String("event_id", env.ID),
			zap.String("event_type", env.Type),
			zap.Error(err),
			zap.NamedError("park_error", parkErr),
			zap.Duration("duration", time.Since(start)),
		)
		return parkErr
	default:
		if p.deduper != nil {
			if markErr := p.deduper.fail(ctx, ledgerID, attempt, err); markErr != nil {
				// The redelivery still happens — the handler error is what
				// decides that — so the lost marker only widens the retry gap.
				// 重投照样发生——决定它的是 handler 错误——丢掉的标记只会拉长重试间隔。
				p.ledgerFailed(lg, "failed", ledgerID, markErr)
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

// runHandler runs the business handler with the ledger lease kept fresh.
//
// Without the renewer, a handler slower than ProcessingLease would let the
// broker's redelivery take the row over while the first copy is still running —
// exactly the overlap the ledger exists to prevent. The renewer writes
// updated_at in database time under the same attempts fence as the outcome
// writes, so a row that was taken over anyway (the process stalled for a whole
// window) stops renewing instead of touching the new owner's row.
//
// The renewer goroutine always ends before this method returns: cancellation
// plus the done channel also cover a panicking handler, so no ticker is left
// renewing a dead claim.
//
// ledgerID is the claim's key, which equals msg.ID unless the id had to be
// fitted to the ledger column; renewing a row under a different key would report
// a stale claim on every tick and stop the lease from being kept alive.
// runHandler 运行 handler，同时保持台账租约不过期。
//
// 没有续期者时，耗时超过 ProcessingLease 的 handler 会被 broker 的重投副本接管——正是台账
// 要消除的重叠。续期者用与结果写回相同的 attempts 围栏、以数据库时间写 updated_at，因此已被
// 接管的行（进程停顿满一个窗口）会停止续期，而不是碰新 owner 的行。
//
// 续期 goroutine 一定在本方法返回前结束：取消加 done 通道也覆盖 handler panic 的路径，
// 不会漏下一个还在为死认领续期的 ticker。
//
// ledgerID 是这次认领的键，除非 ID 需要压进台账列宽，否则与 msg.ID 相同；用另一个键续期
// 会让每一跳都报告认领过期，租约也就无法维持。
func (p *Processor) runHandler(ctx context.Context, msg Message, env Envelope, attempt int, ledgerID string, lg *zap.Logger) error {
	if p.deduper == nil || p.renewInterval <= 0 {
		return p.handler(ctx, msg, env)
	}
	renewCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(p.renewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				err := p.deduper.renew(renewCtx, ledgerID, attempt)
				if err == nil {
					continue
				}
				if errors.Is(err, ErrStaleClaim) {
					// The row moved on and another delivery owns it now.
					// Renewing further would be a no-op at best; stop here and
					// let the outcome write report the stale claim.
					// 行已易主，现在属于另一次投递。继续续期毫无意义；就此停掉，
					// 让收尾写回报告过期认领。
					return
				}
				// A transient database error: keep the ticker running, the next
				// renewal may succeed before the lease runs out.
				// 暂时性写库错误：ticker 继续，下次续期可能在租约到期前成功。
				lg.Warn("consume renew processing lease failed", zap.Error(err))
			}
		}
	}()
	defer func() {
		cancel()
		<-done
	}()
	return p.handler(ctx, msg, env)
}

// ledgerFailed is the single place a post-decision ledger failure is reported:
// a warning in the log and, when configured, the OnLedgerError hook. The
// acknowledgement decision stays what the handler result said — the ledger
// records the outcome, it does not decide it.
// ledgerFailed 是上报"决定之后台账写失败"的唯一入口：日志 Warn，配置了就再调
// OnLedgerError 钩子。确认决定仍由 handler 结果决定——台账只记录结果，不决定结果。
func (p *Processor) ledgerFailed(lg *zap.Logger, marker, messageID string, err error) {
	lg.Warn("consume ledger mark failed",
		zap.String("marker", marker),
		zap.Error(err),
	)
	if p.onLedgerError != nil {
		p.onLedgerError(messageID, err)
	}
}

// Prune deletes this consumer's terminal ledger rows older than olderThan and
// reports how many were removed. Terminal means done or dead; processing and
// failed rows are never pruned, because they are still live claims or carry a
// retry reason.
//
// Call it from a scheduled maintenance loop, not from the consume path. Without
// it the ledger only grows: every message ever consumed keeps a row, and the
// unique index that makes claiming cheap keeps widening.
//
// With idempotency off there is no ledger, so it reports zero and no error. A
// non-positive window is rejected instead of deleting everything: pruning is
// irreversible and a zero window would wipe the terminal history by accident.
// Prune 删除本消费者早于 olderThan 的终态台账行并返回删除条数。终态指 done 与 dead；
// processing 与 failed 永不清理，它们要么仍是活的认领，要么带着待重试的原因。
//
// 请从定时维护任务调用，不要放进消费路径。没有它台账只增不减：每条被消费过的消息都留一行，
// 让认领如此廉价的唯一索引也越来越宽。
//
// 关闭幂等时没有台账，返回 0 与 nil。窗口非正数会被拒绝而不是全删：清理不可逆，零窗口会
// 意外抹掉全部终态历史。
func (p *Processor) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	if p.deduper == nil {
		return 0, nil
	}
	if olderThan <= 0 {
		return 0, ErrInvalidPruneWindow
	}
	return p.deduper.prune(ctx, olderThan)
}

// extract continues the producer's trace when the message carries one.
// extract 在消息携带链路信息时接续生产者的调用链。
func (p *Processor) extract(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	ctx = p.propagator.Extract(ctx, propagation.MapCarrier(headers))
	// X-Trace-Id is the business id and traceparent is the OTel span: they are
	// two systems, so continuing one does not continue the other. Writing the
	// header into ctx here is what lets a handler's logs (and SQL entries) name
	// the request that produced the message.
	// X-Trace-Id 是业务链路 id，traceparent 是 OTel Span：两套体系，接续其中一个不会
	// 接续另一个。把它写回 ctx，处理函数里的日志（与 SQL 条目）才能指向产生这条消息的请求。
	if id := strings.TrimSpace(headers[headerpropagation.HeaderTraceID]); id != "" &&
		headerpropagation.HeaderFromContext(ctx, headerpropagation.HeaderTraceID) == "" {
		ctx = headerpropagation.ContextWithHeader(ctx, headerpropagation.HeaderTraceID, id)
	}
	return ctx
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
		return trace.NewNoopTracerProvider().Tracer("gofy/support/consumer")
	}
	return rt.TracerProvider.Tracer("gofy/support/consumer")
}

// park marks a message as dead so a later redelivery is skipped instead of
// repeating a failure that cannot succeed.
//
// The error is returned rather than logged away: the caller may only
// acknowledge the message once the dead state is durable. ACKing after a failed
// write would delete the broker's copy of a message that never succeeded and
// leave nothing to investigate.
// park 把消息标记为 dead，使后续重投被跳过，而不是重复一次不可能成功的失败。
//
// 错误要返回而不是只记日志：只有 dead 状态落库后，调用方才可以确认消息。写失败仍然 ACK
// 会删掉这条从未成功的消息在 broker 上的副本，也没留下任何可排查的痕迹。
func (p *Processor) park(ctx context.Context, messageID string, attempt int, cause error) error {
	if p.deduper == nil {
		return nil
	}
	if err := p.deduper.park(ctx, messageID, attempt, cause); err != nil {
		return fmt.Errorf("consumer %s: record dead %s: %w", p.consumer, messageID, err)
	}
	return nil
}

// Outcome values stored in worker_consumed_messages.status.
// worker_consumed_messages.status 中保存的结果值。
const (
	consumedStatusProcessing = "processing"
	consumedStatusDone       = "done"
	consumedStatusFailed     = "failed"
	consumedStatusDead       = "dead"
)

// DefaultProcessingLease is how long a processing row blocks a second handler
// when ProcessorConfig.ProcessingLease is zero.
//
// It has to cover the slowest legitimate handler: taking over earlier means the
// broker's redelivered copy runs while the first one is still working, which is
// exactly the overlap this ledger prevents. Shorten it only when the broker
// redelivers faster than that and the handler is known to be quick.
// DefaultProcessingLease 是 ProcessorConfig.ProcessingLease 为零时，一条 processing
// 记录阻止第二个 handler 的时长。
//
// 它必须覆盖最慢的合法处理：过早接管意味着 broker 重投的副本会在第一个副本仍在
// 处理时开始执行，而这正是台账要消除的重叠。只有当 broker 重投更快、且明确知道
// handler 很快时才应调小。
const DefaultProcessingLease = 5 * time.Minute

// DefaultSchemaTimeout bounds the ledger schema initialisation when
// ProcessorConfig.SchemaTimeout is zero.
//
// The statement is a single CREATE TABLE (or a HasTable probe), so 30s is
// already generous; it exists to turn "the database never answers" into a
// startup error instead of a start that hangs.
// DefaultSchemaTimeout 是 ProcessorConfig.SchemaTimeout 为零时台账建表的时限。
//
// 语句只有一条 CREATE TABLE（或一次 HasTable 探测），30 秒已相当宽裕；它的作用是把
// "数据库没有应答"变成启动失败，而不是一直卡在启动里。
const DefaultSchemaTimeout = 30 * time.Second

// inFlightError is the concrete type behind ErrInFlight. It implements the
// queues package's InFlightError marker structurally, which lets a Redis queue
// defer the copy for the processing lease instead of charging the retry budget.
// inFlightError 是 ErrInFlight 的具体类型。它以结构化方式实现 queues 包的
// InFlightError 标记，Redis 队列因此能把副本延后到处理租约之后，而不消耗重试
// 预算。
type inFlightError struct{}

func (inFlightError) Error() string {
	return "consumer: message is being handled by another instance"
}

func (inFlightError) InFlight() {}

var (
	// ErrInFlight reports that another instance holds a valid processing lease
	// for this message. It is deliberately an error: the caller must ask the
	// broker for a redelivery instead of acknowledging, because the copy being
	// handled elsewhere may still fail. A Redis queue recognizes the marker and
	// re-queues the copy after its processing lease without consuming the retry
	// budget.
	// ErrInFlight 表示另一个实例持有该消息有效的 processing 租约。它刻意是错误：
	// 调用方必须请求 broker 重投而不是确认，因为别处正在处理的副本仍可能失败。
	// Redis 队列会识别这个标记，在处理租约之后重新入队，且不消耗重试预算。
	ErrInFlight = inFlightError{}

	// ErrStaleClaim reports that a completion found the row owned by a different
	// attempt. The handler's side effects (if any) already happened, so the
	// message is acknowledged anyway; the error only tells the caller that the
	// ledger was not updated.
	// ErrStaleClaim 表示收尾时发现该行已属于另一次认领。handler 的副作用（如果有）
	// 已经发生，消息照样确认；这个错误只告诉调用方台账没有被更新。
	ErrStaleClaim = errors.New("consumer: message claim was superseded")

	// ErrInvalidPruneWindow means Prune received a non-positive window, which
	// would delete the whole terminal history immediately.
	// ErrInvalidPruneWindow 表示 Prune 收到的窗口不是正数，那会立刻删除全部终态历史。
	ErrInvalidPruneWindow = errors.New("consumer: prune window must be positive")
)

// messageClaim is what claim decided about one delivery.
//
// proceed and inFlight are exclusive: proceed runs the handler with attempt as
// the fencing value, inFlight asks for a redelivery, and neither means the
// message is finished (done or dead) and may be acknowledged.
// messageClaim 是 claim 对一次投递作出的决定。
//
// proceed 与 inFlight 互斥：proceed 用 attempt 作为围栏值执行 handler，inFlight
// 请求 broker 重投，两者都不成立表示消息已终结（done 或 dead），可以确认。
type messageClaim struct {
	proceed  bool
	inFlight bool
	attempt  int
	reason   string
}

// maxLedgerMessageID is the width of the message_id column. The value comes from
// the producer, so it has to be fitted rather than trusted.
// maxLedgerMessageID 是 message_id 列的宽度。写入的值来自生产者，因此必须规整，
// 不能直接相信它的长度。
const maxLedgerMessageID = 128

// ledgerMessageID fits a message id into the ledger column.
//
// AMQP's MessageId is a shortstr the sender chooses, and no adapter can bound it.
// An over-long id reached the insert as-is, which on MySQL is one of two
// failures: in strict mode every attempt fails with "Data too long" and the
// message is redelivered forever (a stalled partition); without strict mode it is
// truncated, so two ids sharing a 128-character prefix land on the same unique
// index entry and the second message is acknowledged as a duplicate — dropped
// silently. A digest is stable across redeliveries, unique enough to keep the
// index honest, and always short enough.
//
// The length is counted in runes because that is what VARCHAR(n) bounds. The
// prefix makes the stored value self-describing: an operator who sees "sha256:..."
// knows why it differs from the id the producer logged.
// ledgerMessageID 把消息 ID 压进台账列宽。
//
// AMQP 的 MessageId 是发送方自行选择的 shortstr，任何适配器都无法假定其长度。超长
// ID 原样进入插入语句后，MySQL 上是两种失败之一：严格模式下每次尝试都报
// "Data too long"，消息被无限重投（分区卡死）；非严格模式下被截断，前缀相同（前 128
// 个字符）的两个 ID 落到同一条唯一索引上，第二条被当成重复消息确认——静默丢失。摘要
// 在多次重投之间稳定、唯一性足够，而且一定放得下。
//
// 长度按字符数计算，因为 VARCHAR(n) 限制的是字符。前缀让存下来的值自解释：运维看到
// "sha256:..." 就知道它为什么与生产者日志里的 ID 不同。
func ledgerMessageID(id string) string {
	if utf8.RuneCountInString(id) <= maxLedgerMessageID {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "sha256:" + hex.EncodeToString(sum[:])
}

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
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	Consumer  string `gorm:"size:64;not null;uniqueIndex:uk_worker_consumed,priority:1"`
	MessageID string `gorm:"size:128;not null;uniqueIndex:uk_worker_consumed,priority:2"`
	Status    string `gorm:"size:16;not null;index:idx_worker_consumed_status"`
	Attempts  int    `gorm:"not null"`
	LastError string `gorm:"size:512"`
	// No explicit SQL type: GORM maps time.Time per dialect. MySQL keeps its
	// millisecond datetime(3) default, PostgreSQL gets timestamptz and SQLite
	// gets datetime; hardcoding datetime(3) made AutoMigrate fail on PostgreSQL.
	// 不写显式 SQL 类型：GORM 按方言映射 time.Time。MySQL 保持默认的毫秒
	// datetime(3)，PostgreSQL 得到 timestamptz，SQLite 得到 datetime；写死
	// datetime(3) 会让 PostgreSQL 上的 AutoMigrate 直接失败。
	CreatedAt time.Time
	UpdatedAt time.Time
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
	lease       time.Duration
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

// claim reserves the message for handling and returns the attempt number that
// fences every later write.
//
// The state machine is:
//
//	absent                        -> processing(attempts=1)         run
//	processing, lease still valid -> nothing changes                 in flight, no ack
//	processing, lease expired     -> processing(attempts+1)         take over
//	failed                        -> processing(attempts+1)         retry
//	done / dead                   -> nothing changes                 skip, ack
//	attempts exhausted            -> dead                            skip, ack
//
// "processing with a live lease" must not be treated like "failed": the first
// handler may still be running, and acknowledging the redelivered copy would
// drop the only backup if that handler dies. It returns ErrInFlight through the
// inFlight flag instead, and the caller asks the broker for another redelivery.
//
// The takeover is a single UPDATE guarded by id + status + attempts, so two
// concurrent redeliveries cannot both enter the handler: the loser sees zero
// affected rows and is treated as in flight.
// claim 为处理预留这条消息，并返回给后续每次写回做围栏的 attempt 序号。
//
// 状态机：
//
//	不存在                        -> processing(attempts=1)         执行
//	processing 且租约仍有效        -> 不改动                          在途，不确认
//	processing 且租约已过期        -> processing(attempts+1)         接管
//	failed                        -> processing(attempts+1)         重试
//	done / dead                   -> 不改动                          跳过并确认
//	attempts 耗尽                 -> dead                            跳过并确认
//
// "processing 且租约有效"不能当成 "failed"：第一个 handler 可能还在跑，确认掉
// 重投的副本就等于在它崩溃时丢掉了唯一的备份。此时通过 inFlight 返回
// ErrInFlight，由调用方请求 broker 再投一次。
//
// 接管是一条由 id + status + attempts 围起来的 UPDATE，两次并发重投不可能都进入
// handler：输的那个看到 0 行受影响，按在途处理。
func (d *deduper) claim(ctx context.Context, messageID string) (messageClaim, error) {
	// The insert takes both timestamps from the database clock. updated_at is
	// the lease itself, so a node whose clock runs ahead would otherwise create a
	// lease that never expires from the point of view of every other node.
	// 插入时两个时间戳都取数据库时钟。updated_at 就是租约本身，否则时钟走快的节点会造出
	// 一条在其他所有节点看来永不过期的租约。
	row := map[string]any{
		"consumer":   d.consumer,
		"message_id": messageID,
		"status":     consumedStatusProcessing,
		"attempts":   1,
		"created_at": d.dbNow(),
		"updated_at": d.dbNow(),
	}
	// DoNothing turns the unique index into a cheap "was I first?" question
	// that also works on SQLite, so tests do not need MySQL-specific SQL.
	// DoNothing 把唯一索引变成一个廉价的"我是第一个吗"判断，在 SQLite 上也能
	// 用，测试因此不必写 MySQL 专有 SQL。
	tx := d.db.WithContext(ctx).Model(&consumedMessage{}).
		Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if tx.Error != nil {
		return messageClaim{}, tx.Error
	}
	if tx.RowsAffected == 1 {
		return messageClaim{proceed: true, attempt: 1}, nil
	}

	var existing consumedMessage
	if err := d.db.WithContext(ctx).
		Where("consumer = ? AND message_id = ?", d.consumer, messageID).
		First(&existing).Error; err != nil {
		return messageClaim{}, err
	}

	switch existing.Status {
	case consumedStatusDone:
		return messageClaim{reason: "already processed"}, nil
	case consumedStatusDead:
		return messageClaim{reason: "already parked as dead"}, nil
	case consumedStatusProcessing:
		// The lease is judged by the database, which is the clock that wrote
		// updated_at in the first place. A node whose clock is off can no longer
		// take over early (both handlers run) or hold a message hostage (nothing
		// takes over).
		// 租约交给数据库判断——updated_at 本来就是数据库时钟写的。时钟偏差的节点因此既不能
		// 提前接管（两个 handler 同时跑），也不会让消息被一直占着（没人接管）。
		expired, err := d.leaseExpired(ctx, existing)
		if err != nil {
			return messageClaim{}, err
		}
		if !expired {
			return messageClaim{inFlight: true}, nil
		}
	case consumedStatusFailed:
		// A recorded failure is the one state that explicitly asks for a
		// retry; fall through to the takeover.
		// 记录为 failed 是唯一明确请求重试的状态，继续走接管流程。
	default:
		// An unknown status cannot be reasoned about, so neither run the
		// handler twice nor acknowledge: ask for a redelivery and let an
		// operator look at the row.
		// 未知状态无法推断，既不重复执行也不确认：请求重投，让人去看这一行。
		return messageClaim{inFlight: true}, nil
	}

	if existing.Attempts >= d.maxAttempts {
		if err := d.markExhausted(ctx, existing); err != nil {
			return messageClaim{}, err
		}
		return messageClaim{reason: "attempts exhausted"}, nil
	}

	// The status and attempt pair is the CAS: only the delivery that read this
	// exact version can move the row forward. A processing row also re-checks the
	// lease inside the UPDATE, because another delivery may have taken over
	// between the read and here — and it would have renewed updated_at.
	// status 与 attempts 这一对就是 CAS：只有读到这个确切版本的投递才能推进该行。processing
	// 的行还要在 UPDATE 里复核一次租约：读到这里之间可能已有别的投递完成了接管，而接管会把
	// updated_at 推后。
	takeover := d.db.WithContext(ctx).Model(&consumedMessage{}).
		Where("id = ? AND status = ? AND attempts = ?", existing.ID, existing.Status, existing.Attempts)
	if existing.Status == consumedStatusProcessing {
		if predicate, ok := d.leaseExpiredPredicate(); ok {
			takeover = takeover.Where(predicate)
		}
	}
	res := takeover.Updates(map[string]any{
		"status":     consumedStatusProcessing,
		"attempts":   gorm.Expr("attempts + 1"),
		"last_error": "",
		"updated_at": d.dbNow(),
	})
	if res.Error != nil {
		return messageClaim{}, res.Error
	}
	if res.RowsAffected != 1 {
		// Someone else won the takeover between the read and the update.
		// 在读与更新之间，别人赢得了这次接管。
		return messageClaim{inFlight: true}, nil
	}
	return messageClaim{proceed: true, attempt: existing.Attempts + 1}, nil
}

// leaseExpired asks the database whether a processing row may be taken over.
//
// updated_at is the implicit lease: it is written on every claim and on every
// outcome in database time, so "last touched longer than the lease ago" means
// the handler that owned it is gone. The comparison has to run in the database
// for the same reason the write does — comparing a database timestamp with this
// node's clock would reintroduce exactly the skew the write avoided.
// leaseExpired 问数据库：一条 processing 记录是否可以被接管。
//
// updated_at 就是隐式租约：每次认领与每次记录结果都用数据库时间写它，因此"最近一次触碰距今
// 超过租约"意味着持有它的 handler 已经不在了。比较必须在数据库里做，理由与写入相同——拿
// 数据库时间戳和本节点时钟比，等于把写入时避开的偏差又请回来。
func (d *deduper) leaseExpired(ctx context.Context, row consumedMessage) (bool, error) {
	if d.lease <= 0 {
		return true, nil
	}
	predicate, ok := d.leaseExpiredPredicate()
	if !ok {
		// No dialect-specific predicate: fall back to this node's clock and say
		// so in the docs rather than pretending the comparison is global.
		// 没有对应方言的表达式时退回本节点时钟，并在文档里说明——不假装这个比较是全局一致的。
		if row.UpdatedAt.IsZero() {
			// A row written outside this package has no lease to trust. Treat it
			// as expired so a stuck message can progress instead of blocking
			// forever.
			// 由本包之外的代码写入的行没有可信任的租约。按过期处理，卡住的消息才能继续
			// 推进，而不是永久阻塞。
			return true, nil
		}
		return time.Since(row.UpdatedAt) >= d.lease, nil
	}
	var expired bool
	err := d.db.WithContext(ctx).Model(&consumedMessage{}).
		Select(predicate).
		Where("id = ?", row.ID).
		Scan(&expired).Error
	if err != nil {
		return false, err
	}
	return expired, nil
}

// renew re-arms the processing lease of a live claim. updated_at is the lease,
// so writing it in database time is the whole operation; the attempts fence
// keeps a superseded handler from touching the new owner's row, which is
// reported as ErrStaleClaim.
// renew 重新武装一条活认领的 processing 租约。updated_at 就是租约，用数据库时间写它就是
// 全部操作；attempts 围栏挡住已被取代的 handler 碰新 owner 的行，命中不到时报
// ErrStaleClaim。
func (d *deduper) renew(ctx context.Context, messageID string, attempt int) error {
	fence := func() *gorm.DB {
		return d.db.WithContext(ctx).
			Where("consumer = ? AND message_id = ? AND attempts = ? AND status = ?",
				d.consumer, messageID, attempt, consumedStatusProcessing)
	}
	res := fence().Model(&consumedMessage{}).
		Updates(map[string]any{"updated_at": d.dbNow()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	// MySQL and SQLite report rows changed, not rows matched: a renewal that
	// lands in the same clock tick as the previous write changes nothing and
	// affects zero rows. Re-read the fence to tell that no-op apart from a lost
	// claim.
	// MySQL 与 SQLite 返回被修改的行数而非匹配行数：与上一次写入落在同一时钟刻度的
	// 续期不改变任何值，影响 0 行。重读围栏，把这种空更新与丢失的认领区分开。
	var cur consumedMessage
	if err := fence().First(&cur).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: message %s attempt %d", ErrStaleClaim, messageID, attempt)
		}
		return err
	}
	return nil
}

// dbNow is the database clock as a SQL expression, used wherever this package
// writes a timestamp the lease later depends on. MySQL must keep millisecond
// precision: the lease predicate compares against CURRENT_TIMESTAMP(3), and a
// second-precision write would look up to 999ms older than it is.
// dbNow 是数据库时钟的 SQL 表达式，本包写入租约依赖的时间戳时都用它。MySQL 必须保留
// 毫秒精度：租约判断按 CURRENT_TIMESTAMP(3) 比较，秒精度的写入会让时间戳最多提前
// 0.999 秒，短租约下会被提前判定过期。
func (d *deduper) dbNow() clause.Expr {
	return dbNowExpr(d.db.Dialector.Name())
}

// dbNowExpr builds the clock expression for a dialect. It is separate from
// deduper so the precision rule can be tested without a database connection.
// dbNowExpr 按方言构造时钟表达式。它与 deduper 分离，精度规则因此不需要数据库连接
// 也能测试。
func dbNowExpr(dialect string) clause.Expr {
	if dialect == "mysql" {
		return gorm.Expr("CURRENT_TIMESTAMP(3)")
	}
	return gorm.Expr("CURRENT_TIMESTAMP")
}

// leaseExpiredPredicate builds the SQL that answers "has this row's lease run
// out", using only the database clock.
// leaseExpiredPredicate 构造只用数据库时钟回答"这行的租约是否过期"的 SQL。
func (d *deduper) leaseExpiredPredicate() (string, bool) {
	return d.elapsedPredicate(d.lease)
}

// elapsedPredicate builds the SQL that answers "was this row last touched at
// least window ago", using only the database clock. The value is built from a
// duration, never from input, so interpolating it is safe; each dialect spells
// interval arithmetic differently, and guessing one spelling for all of them is
// how silent timezone bugs start.
// elapsedPredicate 构造只用数据库时钟回答"这行最近一次触碰是否至少在 window 之前"的 SQL。
// 表达式由时长拼出、不含外部输入，因此直接内联是安全的；各家的区间语法不同，给所有方言用
// 同一种写法正是静默时区 bug 的来源。
func (d *deduper) elapsedPredicate(window time.Duration) (string, bool) {
	if window <= 0 {
		return "1 = 1", true
	}
	micros := window.Microseconds()
	switch d.db.Dialector.Name() {
	case "mysql":
		return fmt.Sprintf("updated_at <= CURRENT_TIMESTAMP(3) - INTERVAL %d MICROSECOND", micros), true
	case "postgres":
		return fmt.Sprintf("updated_at <= CURRENT_TIMESTAMP - %d * INTERVAL '1 microsecond'", micros), true
	case "sqlite":
		// datetime() normalises both sides. The driver stores time.Time with its
		// numeric offset while datetime('now') is UTC without one, and comparing
		// the two as text is wrong in a way that never errors.
		// datetime() 会把两侧归一化。驱动把 time.Time 连同时区偏移一起存成文本，而
		// datetime('now') 是不带偏移的 UTC，按文本比较会错，而且不会报错。
		return fmt.Sprintf("datetime(updated_at) <= datetime('now', '-%.6f seconds')", float64(micros)/1e6), true
	default:
		return "", false
	}
}

// markExhausted moves a row that used up its attempts to the dead state. The
// CAS keeps it from overwriting a row that another delivery is already
// finishing; a zero-row result simply means someone else did it.
// markExhausted 把耗尽重试次数的行转入 dead。CAS 保证它不会覆盖另一个投递正在
// 收尾的行；影响 0 行只说明别人已经做过了。
func (d *deduper) markExhausted(ctx context.Context, row consumedMessage) error {
	return d.db.WithContext(ctx).Model(&consumedMessage{}).
		Where("id = ? AND status = ? AND attempts = ?", row.ID, row.Status, row.Attempts).
		Updates(map[string]any{
			"status":     consumedStatusDead,
			"last_error": "attempts exhausted",
			"updated_at": d.dbNow(),
		}).Error
}

// succeed records a completed message. A zero-row update means the row moved on
// to a different claim (or was already finished by it), and the caller learns
// that through ErrStaleClaim.
// succeed 记录一条已完成的消息。影响 0 行说明该行已归另一次认领（或已由它完成），
// 调用方通过 ErrStaleClaim 得知。
func (d *deduper) succeed(ctx context.Context, messageID string, attempt int) error {
	res := d.db.WithContext(ctx).Model(&consumedMessage{}).
		Where("consumer = ? AND message_id = ? AND attempts = ? AND status = ?",
			d.consumer, messageID, attempt, consumedStatusProcessing).
		Updates(map[string]any{
			"status":     consumedStatusDone,
			"last_error": "",
			"updated_at": d.dbNow(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: message %s attempt %d", ErrStaleClaim, messageID, attempt)
	}
	return nil
}

// fail records a retryable failure so the next attempt sees the reason. Only
// the claim that owns the row may write it, and only while the row is still
// processing: a late failure must never turn a finished message back into one
// that gets retried.
// fail 记录一次可重试失败，让下一次尝试能看到原因。只有持有该行的认领才能写入，
// 且仅当该行仍处于 processing：迟到的失败绝不能把已完成的消息改回待重试。
func (d *deduper) fail(ctx context.Context, messageID string, attempt int, cause error) error {
	res := d.db.WithContext(ctx).Model(&consumedMessage{}).
		Where("consumer = ? AND message_id = ? AND attempts = ? AND status = ?",
			d.consumer, messageID, attempt, consumedStatusProcessing).
		Updates(map[string]any{
			"status":     consumedStatusFailed,
			"last_error": truncate(cause.Error(), 512),
			"updated_at": d.dbNow(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: message %s attempt %d", ErrStaleClaim, messageID, attempt)
	}
	return nil
}

// park records a failure that will not be retried. done and dead are terminal:
// the guard excludes them so a late failure cannot reopen a finished message.
// park 记录一次不再重试的失败。done 与 dead 是终态：条件把它们排除在外，迟到的
// 失败因此无法把已完成的消息重新打开。
func (d *deduper) park(ctx context.Context, messageID string, attempt int, cause error) error {
	res := d.db.WithContext(ctx).Model(&consumedMessage{}).
		Where("consumer = ? AND message_id = ? AND attempts = ? AND status IN ?",
			d.consumer, messageID, attempt,
			[]string{consumedStatusProcessing, consumedStatusFailed}).
		Updates(map[string]any{
			"status":     consumedStatusDead,
			"last_error": truncate(cause.Error(), 512),
			"updated_at": d.dbNow(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: message %s attempt %d", ErrStaleClaim, messageID, attempt)
	}
	return nil
}

// prune deletes terminal rows older than the window, judged by the same
// database clock that wrote updated_at on every claim and outcome. The
// consumer scope keeps one worker's maintenance from deleting another's
// history; done and dead are the only statuses touched.
// prune 删除早于窗口的终态行，比较用的是每次认领与收尾写入 updated_at 的同一个数据库
// 时钟。按消费者限定，避免一个 worker 的维护删掉另一个 worker 的历史；只碰 done 与
// dead 两种状态。
func (d *deduper) prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	query := d.db.WithContext(ctx).
		Where("consumer = ?", d.consumer).
		Where("status IN ?", []string{consumedStatusDone, consumedStatusDead})
	if predicate, ok := d.elapsedPredicate(olderThan); ok {
		query = query.Where(predicate)
	} else {
		// No dialect-specific predicate: fall back to this node's clock. Skew
		// only shifts the cut-off, so a row is never deleted more than the skew
		// too early.
		// 没有对应方言的表达式时退回本节点时钟。偏差只会平移截止点，最多让一行提前
		// 被删掉与偏差相当的时间。
		query = query.Where("updated_at < ?", time.Now().Add(-olderThan))
	}
	res := query.Delete(&consumedMessage{})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
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
