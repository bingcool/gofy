package consumer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"

	gofykafka "github.com/bingcool/gofy/support/kafka"
	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/bingcool/gofy/support/worker/consumer/kafkaconsumer"
)

// KafkaConfig describes one Kafka subscription.
//
// A consumer is a file, not a block in a shared config: a service that reads
// three topics has three KafkaConfig values in three files, each next to the
// handler it feeds. The zero value of every field below means "the default",
// so a consumer only writes what makes it different — usually Name, Topic,
// Group and Handler.
// KafkaConfig 描述一个 Kafka 订阅。
//
// 一个消费者是一个文件，而不是共享配置里的一个块：读三个 topic 的服务就有三个
// KafkaConfig 值、分在三个文件里，各自紧挨着它喂的 handler。下面每个字段的零值都表示
// "用默认值"，因此消费者只写让它与众不同的部分——通常是 Name、Topic、Group 与 Handler。
type KafkaConfig struct {
	// Name is the worker name: it appears in `worker --list`, in the log
	// fields, in the pid file name and as the idempotency scope. Required, and
	// unique inside the binary.
	// Name 是 worker 名：出现在 `worker --list`、日志字段、pid 文件名与幂等作用域里。
	// 必填，且在同一个二进制内唯一。
	Name string
	// Concurrency is the number of consumer instances in this process, which is
	// also the number of franz-go clients: raising it means more members in the
	// same consumer group, useful only when the topic has enough partitions.
	// Concurrency 是进程内的消费者实例数，同时也是 franz-go 客户端的数量：调大意味着
	// 同一个消费组里更多成员，只有 topic 分区足够多才有意义。
	Concurrency int
	// Idempotency turns a redelivery into a no-op by claiming the message in
	// the consumed-message table. It needs a database; leaving it off means the
	// handler itself must be idempotent.
	// Idempotency 通过在消费台账里认领消息，把重复投递变成空操作。它需要数据库；
	// 关闭它意味着 handler 自己必须幂等。
	Idempotency bool
	// MaxAttempts bounds how often one message is handled before it is parked
	// as dead. Zero means five.
	// MaxAttempts 限制同一条消息的处理次数，超过后记为 dead。零值表示五次。
	MaxAttempts int
	// Instance names the kafka.<instance> connection block to use. Empty means
	// "default"; brokers and timeouts are configured once there.
	// Instance 指定使用的 kafka.<实例> 连接段。为空表示 "default"；broker 与超时
	// 在那里配一次。
	Instance string
	// Topic is the subscribed topic. Required.
	// Topic 是订阅的 topic。必填。
	Topic string
	// Group is the consumer group; offsets are committed per group. Required.
	// Group 是消费组，位点按组提交。必填。
	Group string
	// CommitMode is manual or auto. Manual commits only what the handler
	// accepted, which is the safe default for business events.
	// CommitMode 取 manual 或 auto。manual 只提交 handler 处理成功的记录，
	// 对业务事件是安全默认值。
	CommitMode string
	// AutoOffsetReset is earliest or latest, used when the group has no
	// committed offset yet.
	// AutoOffsetReset 取 earliest 或 latest，仅在消费组还没有已提交位点时生效。
	AutoOffsetReset string
	// AutoCommitInterval is how often offsets are auto committed; it matters
	// only when CommitMode is auto. Zero means 200ms, the same default
	// franz-go uses.
	// AutoCommitInterval 是自动提交位点的间隔，仅在 CommitMode 为 auto 时生效。
	// 0 表示 200ms，与 franz-go 的默认值一致。
	AutoCommitInterval time.Duration
	// SessionTimeout is how long the broker waits for a heartbeat before
	// removing this member and starting a rebalance. Zero uses 45s.
	// SessionTimeout 是 broker 等心跳的时长，超时会把本成员移出组并触发重平衡。
	// 0 表示 45s。
	SessionTimeout time.Duration
	// BlockRebalanceOnPoll finishes the current batch — handler, then commit —
	// before a rebalance may proceed.
	//
	// franz-go has no max.poll.interval.ms to raise: the Java client stops
	// heartbeating while the application thread runs, while franz-go heartbeats
	// on its own goroutine, so a slow handler never drops the member out of the
	// group. What can still happen is a rebalance landing between the handler
	// and its commit, which fails the commit and replays the batch. This turns
	// that into "let the batch finish".
	// BlockRebalanceOnPoll 让当前批次——先 handler 后提交——跑完再放行重平衡。
	//
	// franz-go 没有可调大的 max.poll.interval.ms：Java 客户端在应用线程执行期间
	// 停止心跳，而 franz-go 由独立 goroutine 心跳，慢 handler 不会把成员踢出组。
	// 仍可能发生的是重平衡落在 handler 与它的提交之间，导致提交失败、整批重放。
	// 这个开关把它变成"等这一批跑完"。
	BlockRebalanceOnPoll bool
	// MaxRecords caps one poll; zero polls everything already buffered.
	// MaxRecords 限制单次拉取条数，0 表示拉取全部已缓冲记录。
	MaxRecords int
	// RetryDelay pauses after a poll error that returned no records.
	// RetryDelay 是拉取出错且没有返回记录时的等待时间。
	RetryDelay time.Duration
	// DiscardOnError commits a failed record and moves on instead of stopping
	// the consumer. Off by default: a stopped consumer is visible, a skipped
	// event is not.
	// DiscardOnError 提交失败记录并继续，而不是停下消费者。默认关闭：停下的消费者
	// 看得见，被跳过的事件看不见。
	DiscardOnError bool
	// Handler runs the business branch. Required.
	// Handler 执行业务分支。必填。
	Handler KafkaHandlerFunc
}

// KafkaMessage is one record as the handler sees it: the decoded envelope, the
// delivery count and the native record.
//
// Record is there for what the envelope does not carry — partition, offset,
// headers, timestamp — and for a manual commit: it is the value
// kgo.Client.CommitRecords takes.
// KafkaMessage 是 handler 看到的单条记录：解码后的信封、投递次数与原生记录。
//
// Record 提供信封里没有的东西——分区、位点、消息头、时间戳——也用于手动提交：它就是
// kgo.Client.CommitRecords 接收的值。
type KafkaMessage struct {
	Envelope
	// Attempt counts deliveries. Kafka does not report one, so it stays at 1;
	// the idempotency ledger is what bounds repeated handling here.
	// Attempt 是投递次数。Kafka 不报告这个值，因此固定为 1；在这里限制重复处理的是
	// 幂等台账。
	Attempt int32
	// Record is the native record.
	// Record 是原生记录。
	Record *kgo.Record
}

// KafkaHandlerFunc processes one Kafka record.
//
// The subscription's own config travels with the message, so a handler can read
// its topic, group or idempotency switch without capturing the variable it was
// declared next to. That keeps handleOrderKafka a plain function — testable on
// its own — instead of a closure over the consumer.
// KafkaHandlerFunc 处理一条 Kafka 记录。
//
// 订阅自己的配置随消息一起传入，handler 因此不用闭包捕获声明处的变量，就能读到自己的
// topic、group 或幂等开关。这样 handleOrderKafka 仍是普通函数、可以单独测试，而不是
// 一个包住消费者的闭包。
type KafkaHandlerFunc func(ctx context.Context, cfg KafkaConfig, msg KafkaMessage) error

// KafkaConsumer is one subscription, ready to be put in a worker group.
//
// It satisfies worker.Worker (Name, Run) and worker.Concurrent (Concurrency)
// without importing that package: the group constructor accepts it as-is.
// KafkaConsumer 是一个订阅，可以直接放进 worker 分组。
//
// 它满足 worker.Worker（Name、Run）与 worker.Concurrent（Concurrency），但不必导入
// 那个包：分组构造函数可以直接接收它。
type KafkaConsumer struct {
	cfg  KafkaConfig
	conn gofykafka.Config
}

// NewKafka validates cfg, resolves its connection block and returns the
// consumer. It does not connect: Run does, so `worker --list` and a consumer
// that is filtered out cost nothing.
// NewKafka 校验 cfg、解析它的连接段并返回消费者。它不建连：建连在 Run 里，因此
// `worker --list` 与被过滤掉的消费者都不产生成本。
func NewKafka(conns Connections, cfg KafkaConfig) (*KafkaConsumer, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("kafka consumer %q: %w", cfg.Name, err)
	}
	conn, err := conns.ResolveKafka(cfg.Instance)
	if err != nil {
		return nil, fmt.Errorf("kafka consumer %q: %w", cfg.Name, err)
	}
	return &KafkaConsumer{cfg: cfg, conn: conn}, nil
}

// Name returns the worker name.
// Name 返回 worker 名。
func (c *KafkaConsumer) Name() string { return c.cfg.Name }

// ReportsReady tells the runner that Run reports readiness itself: this
// consumer connects and subscribes inside Run, so "Run was entered" is not the
// same as "the process can accept work".
// ReportsReady 告诉 runner：Run 自行上报就绪。该消费者在 Run 内建连并订阅，因此
// "Run 已进入"并不等于"进程可以接活"。
func (c KafkaConsumer) ReportsReady() {}

// Concurrency returns how many instances the worker should run.
// Concurrency 返回 worker 应当运行的实例数。
func (c *KafkaConsumer) Concurrency() uint8 { return concurrency(c.cfg.Concurrency) }

// Run owns the subscription: it connects, consumes until ctx is cancelled and
// releases both the client and the consumer on the way out.
// Run 拥有订阅本身：建连、消费到 ctx 取消，并在退出时释放客户端与消费者。
func (c *KafkaConsumer) Run(ctx context.Context) error {
	name := c.cfg.Name
	proc, lg, err := start(ctx, name, c.cfg.Idempotency, c.cfg.MaxAttempts, c.handle)
	if err != nil {
		return err
	}

	client, err := gofykafka.New(ctx, c.conn)
	if err != nil {
		return fmt.Errorf("%s: connect: %w", name, err)
	}
	defer func() { _ = client.Close() }()

	consumer, err := gofykafka.NewConsumer(client, gofykafka.ConsumerConfig{
		Topic:                c.cfg.Topic,
		Group:                c.cfg.Group,
		CommitMode:           gofykafka.CommitMode(c.cfg.CommitMode),
		AutoOffsetReset:      c.cfg.AutoOffsetReset,
		AutoCommitInterval:   c.cfg.AutoCommitInterval,
		SessionTimeout:       c.cfg.SessionTimeout,
		BlockRebalanceOnPoll: c.cfg.BlockRebalanceOnPoll,
	})
	if err != nil {
		return fmt.Errorf("%s: subscribe %s: %w", name, c.cfg.Topic, err)
	}
	// The adapter closes the consumer on shutdown; this second close covers the
	// restart path, where the adapter deliberately leaves it open for reuse.
	// 适配器在停机时关闭消费者；这里的第二次关闭覆盖重启路径——适配器刻意不关是留给
	// 复用，而本实现每次重启都会重建，旧对象必须释放。
	defer func() { _ = consumer.Close() }()

	w, err := kafkaconsumer.New(name, consumer, kafkaconsumer.Config{
		Handler: func(ctx context.Context, record *kgo.Record) error {
			return proc.Handle(ctx, kafkaMessage(record))
		},
		Options: gofykafka.ConsumeOptions{
			DiscardOnError: c.cfg.DiscardOnError,
			MaxRecords:     c.cfg.MaxRecords,
			RetryDelay:     c.cfg.RetryDelay,
			OnError: func(err error) {
				lg.Error("kafka consume error", zap.Error(err))
			},
		},
	})
	if err != nil {
		return err
	}

	// The client is lazy: New does not dial, and the consume loop only fails
	// once it polls. A broker that cannot be reached is exactly the state the
	// probe must not call ready, so verify the connection first.
	// 客户端是惰性的：New 不拨号，消费循环要等到 poll 才会暴露失败。broker 不可达
	// 正是探针不能报就绪的状态，因此先确认一次连接。
	if err := client.Ping(ctx); err != nil {
		return fmt.Errorf("%s: ping broker: %w", name, err)
	}
	gofyworker.Ready(ctx)

	lg.Info("kafka consumer started",
		zap.String("topic", c.cfg.Topic),
		zap.String("group", c.cfg.Group),
		zap.String("commit_mode", c.cfg.CommitMode),
		zap.Bool("idempotency", c.cfg.Idempotency),
	)
	defer lg.Info("kafka consumer stopped", zap.String("topic", c.cfg.Topic))

	return w.Run(ctx)
}

// handle adapts the shared chain's callback to the typed handler: the native
// record travels through Message.Native, and the config is the one the consumer
// was built with.
// handle 把共用链路的回调适配成有类型的 handler：原生记录经 Message.Native 传过来，
// 配置就是构造消费者时用的那一份。
func (c *KafkaConsumer) handle(ctx context.Context, message Message, envelope Envelope) error {
	record, _ := message.Native.(*kgo.Record)
	return c.cfg.Handler(ctx, c.cfg, KafkaMessage{
		Envelope: envelope,
		Attempt:  message.Attempt,
		Record:   record,
	})
}

// withDefaults fills the fields a consumer usually leaves alone.
// withDefaults 填充消费者通常不写的字段。
func (c KafkaConfig) withDefaults() KafkaConfig {
	if c.Instance == "" {
		c.Instance = "default"
	}
	if c.Concurrency == 0 {
		c.Concurrency = 1
	}
	if c.CommitMode == "" {
		c.CommitMode = string(gofykafka.CommitModeManual)
	}
	if c.AutoOffsetReset == "" {
		c.AutoOffsetReset = "earliest"
	}
	if c.SessionTimeout <= 0 {
		c.SessionTimeout = 45 * time.Second
	}
	// Exactly zero, not <= 0: a negative value stays negative so validate can
	// reject it, instead of being quietly replaced by the default.
	// 只处理等于零的情况，不用 <= 0：负值保持原样，交给 validate 报错，而不是被悄悄
	// 换成默认值。
	if c.AutoCommitInterval == 0 {
		c.AutoCommitInterval = 200 * time.Millisecond
	}
	if c.RetryDelay <= 0 {
		c.RetryDelay = time.Second
	}
	if c.MaxAttempts < 1 {
		c.MaxAttempts = 5
	}
	return c
}

func (c KafkaConfig) validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("name is required")
	}
	if !validName(c.Name) {
		return fmt.Errorf("invalid worker name %q", c.Name)
	}
	if strings.TrimSpace(c.Topic) == "" {
		return errors.New("topic is required")
	}
	if strings.TrimSpace(c.Group) == "" {
		return errors.New("group is required")
	}
	if c.Handler == nil {
		return errors.New("handler is required")
	}
	if err := validateConcurrency(c.Concurrency); err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(c.CommitMode)) {
	case "manual", "auto":
	default:
		return fmt.Errorf("commit_mode must be manual or auto, got %q", c.CommitMode)
	}
	switch strings.ToLower(strings.TrimSpace(c.AutoOffsetReset)) {
	case "earliest", "latest":
	default:
		return fmt.Errorf("auto_offset_reset must be earliest or latest, got %q", c.AutoOffsetReset)
	}
	// A negative commit interval would reach franz-go as a timer that fires
	// immediately. SessionTimeout and RetryDelay are clamped by withDefaults
	// instead, because their zero value already means "use the default".
	// 负的提交间隔到 franz-go 那里会变成立刻触发的定时器。SessionTimeout 与
	// RetryDelay 由 withDefaults 夹紧——它们的零值本身就表示"用默认值"。
	if c.AutoCommitInterval < 0 {
		return errors.New("auto commit interval must not be negative")
	}
	return nil
}

// kafkaMessage converts one Kafka record into the shared message view.
// kafkaMessage 把一条 Kafka 记录转成共用的消息视图。
func kafkaMessage(record *kgo.Record) Message {
	headers := make(map[string]string, len(record.Headers))
	for _, header := range record.Headers {
		headers[header.Key] = string(header.Value)
	}
	return Message{
		ID:       kafkaMessageID(record, headers),
		Source:   "kafka",
		Topic:    record.Topic,
		Key:      string(record.Key),
		Body:     record.Value,
		Attempt:  1,
		Headers:  headers,
		Enqueued: record.Timestamp,
		Native:   record,
	}
}

// kafkaMessageID prefers the producer's message id and falls back to the
// partition offset.
//
// The offset is stable for one record, so a redelivery of the same record
// deduplicates; a producer that re-sends the same business event under a new
// offset must set the message_id header for that to hold.
// kafkaMessageID 优先用生产者的 message id，缺失时回退到分区位点。
//
// 位点对同一条记录是稳定的，因此同一条记录的重投能去重；生产者若用新位点重发同一个
// 业务事件，就必须设置 message_id 头，去重才成立。
func kafkaMessageID(record *kgo.Record, headers map[string]string) string {
	if id := headers["message_id"]; id != "" {
		return id
	}
	return fmt.Sprintf("%s/%d@%d", record.Topic, record.Partition, record.Offset)
}
