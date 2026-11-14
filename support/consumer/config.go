// Package consumer is the shared half of a resident consumer: the configuration
// block, the message envelope, a database-backed idempotency ledger and the
// permanent-versus-retryable split.
//
// The protocol adapters in support/worker/consumer turn a Kafka record, an AMQP
// delivery, a RocketMQ message or a Redis queue entry into a Message and hand it
// to Processor.Handle; everything after that is written once, here. An
// application supplies the configuration, a HandlerFunc for its business events,
// and the connection it already holds for the broker.
//
// The chain takes a *core.Runtime rather than its pieces one by one, so a
// consumed message logs, traces and writes through exactly what the worker host
// built — not a second logger, tracer or pool.
// 包 consumer 是常驻消费者的共用部分：配置块、消息信封、基于数据库的幂等台账，以及
// "永久失败 / 可重试"的区分。
//
// support/worker/consumer 里的协议适配器把 Kafka 记录、AMQP 投递、RocketMQ 消息或
// Redis 队列项变成 Message，交给 Processor.Handle；之后的一切只在这里写一次。应用只需
// 提供配置、处理业务事件的 HandlerFunc，以及它本来就持有的 broker 连接。
//
// 链路接收 *core.Runtime 而不是逐个传入组件，是为了让消费到的消息用宿主构造的同一个
// logger、tracer 与连接池，而不是另起一套。
package consumer

import (
	"errors"
	"fmt"
	"strings"
	"time"

	gofyamqp "github.com/bingcool/gofy/support/amqp"
	gofykafka "github.com/bingcool/gofy/support/kafka"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
)

// Config is the consumer block of an application's worker configuration: which
// of the four kinds are on, and how each one subscribes.
//
// A scan loop takes no configuration — its interval is a number only its own
// loop cares about. A consumer is different: its topic, its consumer group and
// its broker are deployment facts that differ between environments, so they
// belong in a config file while the JSON shape of the payload stays in code.
//
// Decode it with config.Load and the default tags apply; Validate runs as part
// of decoding, so a missing topic fails before a pool or a log file exists.
// Config 是应用 worker 配置里的消费者段：四种消费者哪些开启，每一种如何订阅。
//
// 扫描循环不接收配置——扫描间隔只有它自己的循环关心。消费者不同：topic、消费组与
// broker 都是随环境变化的部署事实，应该写在配置文件里；消息体长什么样属于代码。
//
// 用 config.Load 解码，default 标签会自动生效；Validate 在解码过程中执行，因此缺
// topic 会在连接池和日志文件出现之前就失败。
type Config struct {
	Consumers Consumers `yaml:"consumers"`
}

// Consumers groups the four consumer kinds the demo supports. Each one is
// independent: enabling Kafka never starts an AMQP consumer, and a broker that
// is not configured only means that kind stays off.
// Consumers 汇总示例支持的四种消费者。四者彼此独立：启用 Kafka 不会顺带启动
// AMQP 消费者，没有配置的 broker 只意味着那一类保持关闭。
type Consumers struct {
	Kafka    KafkaConsumer    `yaml:"kafka"`
	AMQP     AMQPConsumer     `yaml:"amqp"`
	RocketMQ RocketMQConsumer `yaml:"rocketmq"`
	Queues   QueuesConsumer   `yaml:"queues"`
}

// KafkaConsumer describes one Kafka subscription.
//
// One worker instance owns one consumer, so Concurrency is also the number of
// franz-go clients: raising it means more members in the same consumer group,
// which is only useful when the topic has enough partitions.
// KafkaConsumer 描述一个 Kafka 订阅。
//
// 一个 worker 实例独占一个消费者，因此 Concurrency 同时也是 franz-go 客户端
// 的数量：调大意味着同一个消费组里更多成员，只有 topic 分区足够多才有意义。
type KafkaConsumer struct {
	Enabled bool `yaml:"enabled"`
	// Instance names the kafka.<instance> connection block to reuse. Brokers
	// and timeouts are configured once there, not repeated here.
	// Instance 指向复用的 kafka.<实例> 连接段。broker 与超时在那里配一次，
	// 不在这里重复。
	Instance string `yaml:"instance" default:"default"`
	// Concurrency is the number of consumer instances inside the process.
	// Concurrency 是进程内的消费者实例数。
	Concurrency int `yaml:"concurrency" default:"1"`
	// Topic is the subscribed topic.
	// Topic 是订阅的 topic。
	Topic string `yaml:"topic"`
	// Group is the consumer group; offsets are committed per group.
	// Group 是消费组，位点按组提交。
	Group string `yaml:"group"`
	// CommitMode is manual or auto. Manual commits only what the handler
	// accepted, which is the safe default for business events.
	// CommitMode 取 manual 或 auto。manual 只提交 handler 处理成功的记录，
	// 对业务事件是安全默认值。
	CommitMode string `yaml:"commit_mode" default:"manual"`
	// AutoOffsetReset is earliest or latest, used when the group has no
	// committed offset yet.
	// AutoOffsetReset 取 earliest 或 latest，仅在消费组还没有已提交位点时生效。
	AutoOffsetReset string        `yaml:"auto_offset_reset" default:"earliest"`
	SessionTimeout  time.Duration `yaml:"session_timeout" default:"45s"`
	// MaxRecords caps one poll; zero polls everything already buffered.
	// MaxRecords 限制单次拉取条数，0 表示拉取全部已缓冲记录。
	MaxRecords int `yaml:"max_records"`
	// RetryDelay pauses after a poll error that returned no records.
	// RetryDelay 是拉取出错且没有返回记录时的等待时间。
	RetryDelay time.Duration `yaml:"retry_delay" default:"1s"`
	// DiscardOnError commits a failed record and moves on instead of stopping
	// the consumer. Off by default: a stopped consumer is visible, a skipped
	// event is not.
	// DiscardOnError 提交失败记录并继续，而不是停下消费者。默认关闭：停下的
	// 消费者看得见，被跳过的事件看不见。
	DiscardOnError bool `yaml:"discard_on_error"`
	// Idempotency enables the consumed-message table, which turns a redelivery
	// into a no-op instead of a second side effect.
	// Idempotency 开启消费记录表，把重复投递变成空操作，而不是第二次副作用。
	Idempotency bool `yaml:"idempotency" default:"true"`
	// MaxAttempts bounds how often one message is handled before it is parked
	// as dead in the consumed-message table.
	// MaxAttempts 限制同一条消息的处理次数，超过后记为 dead 不再消费。
	MaxAttempts int `yaml:"max_attempts" default:"5"`
}

// AMQPConsumer describes one RabbitMQ queue subscription. The topology is
// declared by the consumer, so the queue exists before the first message is
// published to it.
// AMQPConsumer 描述一个 RabbitMQ 队列订阅。topology 由消费者声明，因此队列在
// 第一条消息发布之前就已存在。
type AMQPConsumer struct {
	Enabled     bool   `yaml:"enabled"`
	Instance    string `yaml:"instance" default:"default"`
	Concurrency int    `yaml:"concurrency" default:"1"`
	// Exchange, ExchangeType, Queue, BindingKey and RoutingKey mirror
	// amqp.Topology. A direct exchange requires RoutingKey == BindingKey; an
	// empty RoutingKey is copied from BindingKey.
	// Exchange、ExchangeType、Queue、BindingKey、RoutingKey 对应
	// amqp.Topology。direct exchange 要求 RoutingKey == BindingKey；RoutingKey
	// 为空时会从 BindingKey 复制。
	Exchange     string `yaml:"exchange"`
	ExchangeType string `yaml:"exchange_type" default:"direct"`
	Queue        string `yaml:"queue"`
	BindingKey   string `yaml:"binding_key"`
	RoutingKey   string `yaml:"routing_key"`
	// Durable keeps the queue and its messages across a broker restart.
	// Durable 让队列及其消息在 broker 重启后仍然存在。
	Durable bool `yaml:"durable" default:"true"`
	// PrefetchCount is the unacknowledged delivery window. Raising it is
	// cheaper than adding consumer instances.
	// PrefetchCount 是未确认投递的窗口。调大它比增加消费者实例更划算。
	PrefetchCount int `yaml:"prefetch_count" default:"32"`
	// AutoAck disables manual acknowledgement; the handler result then only
	// affects logging, not redelivery.
	// AutoAck 关闭手动确认，此时 handler 结果只影响日志，不影响重投。
	AutoAck        bool          `yaml:"auto_ack"`
	DiscardOnError bool          `yaml:"discard_on_error"`
	ReconnectDelay time.Duration `yaml:"reconnect_delay" default:"3s"`
	Idempotency    bool          `yaml:"idempotency" default:"true"`
	MaxAttempts    int           `yaml:"max_attempts" default:"5"`
}

// RocketMQConsumer describes one RocketMQ subscription. A RocketMQ consumer is
// single-use, so Concurrency stays at one worker instance and throughput comes
// from PullWorkers inside the SDK.
// RocketMQConsumer 描述一个 RocketMQ 订阅。RocketMQ 消费者是一次性对象，因此
// worker 实例数保持 1，吞吐由 SDK 内部的 PullWorkers 提供。
type RocketMQConsumer struct {
	Enabled     bool   `yaml:"enabled"`
	Instance    string `yaml:"instance" default:"default"`
	Concurrency int    `yaml:"concurrency" default:"1"`
	Topic       string `yaml:"topic"`
	Group       string `yaml:"group"`
	// Tags restricts the subscription; empty subscribes to every tag.
	// Tags 限定订阅范围，为空表示订阅全部 tag。
	Tags []string `yaml:"tags"`
	// PullWorkers is the SDK's receive concurrency, not the worker's.
	// PullWorkers 是 SDK 的拉取并发，不是 worker 的实例数。
	PullWorkers       int           `yaml:"pull_workers" default:"1"`
	BatchSize         int           `yaml:"batch_size" default:"16"`
	AwaitDuration     time.Duration `yaml:"await_duration" default:"5s"`
	InvisibleDuration time.Duration `yaml:"invisible_duration" default:"30s"`
	DiscardOnError    bool          `yaml:"discard_on_error"`
	Idempotency       bool          `yaml:"idempotency" default:"true"`
	MaxAttempts       int           `yaml:"max_attempts" default:"5"`
}

// QueuesConsumer describes one Redis queue subscription. The queue is
// at-most-once for in-flight work: a popped message is already gone from Redis,
// so a crash mid-handler loses it. Use Kafka/AMQP when that is unacceptable.
// QueuesConsumer 描述一个 Redis 队列订阅。该队列对在途消息是"至多一次"：消息
// 一旦弹出就已从 Redis 删除，处理中崩溃会丢这一条。不能接受的场景请用
// Kafka/AMQP。
type QueuesConsumer struct {
	Enabled bool `yaml:"enabled"`
	// RedisInstance names the redis.<instance> connection block to reuse.
	// RedisInstance 指向复用的 redis.<实例> 连接段。
	RedisInstance string `yaml:"redis_instance" default:"ratelimit"`
	// Concurrency can be raised freely: every instance pops independently and
	// the pop itself is atomic.
	// Concurrency 可以放心调大：每个实例独立弹出，弹出本身是原子的。
	Concurrency int    `yaml:"concurrency" default:"2"`
	Key         string `yaml:"key"`
	// MaxRetries is how often a failed message re-enters the main queue before
	// the queue drops it.
	// MaxRetries 是失败消息重新进入主队列的次数，超过后队列会丢弃它。
	MaxRetries     int           `yaml:"max_retries" default:"3"`
	IdleDelay      time.Duration `yaml:"idle_delay" default:"200ms"`
	RetryDelay     time.Duration `yaml:"retry_delay" default:"3s"`
	DiscardOnError bool          `yaml:"discard_on_error"`
	Idempotency    bool          `yaml:"idempotency" default:"true"`
	MaxAttempts    int           `yaml:"max_attempts" default:"5"`
}

// Errors reported by Validate. They are wrapped with the config path so the
// startup message names the exact key to fix.
// Validate 返回的错误。它们会带上配置路径，启动报错时能直接指出要改哪个键。
var (
	// ErrTopicRequired means a consumer was enabled without a topic.
	// ErrTopicRequired 表示消费者已启用但没有配置 topic。
	ErrTopicRequired = errors.New("topic is required")
	// ErrGroupRequired means a Kafka or RocketMQ consumer has no group.
	// ErrGroupRequired 表示 Kafka 或 RocketMQ 消费者没有配置消费组。
	ErrGroupRequired = errors.New("group is required")
	// ErrQueueRequired means an AMQP consumer has no queue name.
	// ErrQueueRequired 表示 AMQP 消费者没有配置队列名。
	ErrQueueRequired = errors.New("queue is required")
	// ErrExchangeRequired means an AMQP consumer has no exchange.
	// ErrExchangeRequired 表示 AMQP 消费者没有配置 exchange。
	ErrExchangeRequired = errors.New("exchange is required")
	// ErrKeyRequired means a Redis queue consumer has no queue key.
	// ErrKeyRequired 表示 Redis 队列消费者没有配置队列 key。
	ErrKeyRequired = errors.New("key is required")
	// ErrInstanceRequired means a consumer does not name a connection block.
	// ErrInstanceRequired 表示消费者没有指定要复用的连接段。
	ErrInstanceRequired = errors.New("instance is required")
)

// Validate checks the enabled consumers. Disabled blocks are ignored, so a
// shipped config can keep all four visible with enabled: false and still pass.
//
// The checks run in a fixed order instead of over a map: with two blocks
// misconfigured, a map reports whichever one Go happens to visit first, so the
// same file would produce a different message on each start. The prefix is the
// field path — consumers.kafka — which is also where the key sits in a config
// file that maps this struct at its root.
// Validate 校验已启用的消费者。未启用的配置块直接跳过，因此仓库里的示例配置
// 可以把四种都列出来并保持 enabled: false。
//
// 按固定顺序检查而不是遍历 map：两个配置块同时写错时，map 会报告 Go 恰好先访问的
// 那一个，同一个文件每次启动给出不同报错。前缀是字段路径 consumers.kafka，也就是把
// 这个结构映射在根上的配置文件里该键所在的位置。
func (c Config) Validate() error {
	blocks := []struct {
		path  string
		check func() error
	}{
		{"consumers.kafka", c.Consumers.Kafka.validate},
		{"consumers.amqp", c.Consumers.AMQP.validate},
		{"consumers.rocketmq", c.Consumers.RocketMQ.validate},
		{"consumers.queues", c.Consumers.Queues.validate},
	}
	for _, block := range blocks {
		if err := block.check(); err != nil {
			return fmt.Errorf("%s: %w", block.path, err)
		}
	}
	return nil
}

func (c KafkaConsumer) validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Topic) == "" {
		return ErrTopicRequired
	}
	if strings.TrimSpace(c.Group) == "" {
		return ErrGroupRequired
	}
	if err := validateCommon(c.Instance, c.Concurrency, c.MaxAttempts); err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(c.CommitMode)) {
	case "", "manual", "auto":
	default:
		return fmt.Errorf("commit_mode must be manual or auto, got %q", c.CommitMode)
	}
	switch strings.ToLower(strings.TrimSpace(c.AutoOffsetReset)) {
	case "", "earliest", "latest":
	default:
		return fmt.Errorf("auto_offset_reset must be earliest or latest, got %q", c.AutoOffsetReset)
	}
	return nil
}

func (c AMQPConsumer) validate() error {
	if !c.Enabled {
		return nil
	}
	if err := validateCommon(c.Instance, c.Concurrency, c.MaxAttempts); err != nil {
		return err
	}
	if strings.TrimSpace(c.Exchange) == "" {
		return ErrExchangeRequired
	}
	if strings.TrimSpace(c.Queue) == "" {
		return ErrQueueRequired
	}
	switch strings.ToLower(strings.TrimSpace(c.ExchangeType)) {
	case "", "direct", "fanout", "topic", "headers":
	default:
		return fmt.Errorf("exchange_type must be direct, fanout, topic or headers, got %q", c.ExchangeType)
	}
	if strings.EqualFold(c.ExchangeType, "direct") {
		routingKey, bindingKey := strings.TrimSpace(c.RoutingKey), strings.TrimSpace(c.BindingKey)
		if routingKey == "" && bindingKey == "" {
			return errors.New("direct exchange requires routing_key or binding_key")
		}
		if routingKey != "" && bindingKey != "" && routingKey != bindingKey {
			return errors.New("direct exchange requires routing_key == binding_key")
		}
	}
	if c.PrefetchCount < 0 {
		return errors.New("prefetch_count must not be negative")
	}
	return nil
}

func (c RocketMQConsumer) validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Topic) == "" {
		return ErrTopicRequired
	}
	if strings.TrimSpace(c.Group) == "" {
		return ErrGroupRequired
	}
	if err := validateCommon(c.Instance, c.Concurrency, c.MaxAttempts); err != nil {
		return err
	}
	if c.PullWorkers < 0 {
		return errors.New("pull_workers must not be negative")
	}
	return nil
}

func (c QueuesConsumer) validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Key) == "" {
		return ErrKeyRequired
	}
	if strings.TrimSpace(c.RedisInstance) == "" {
		return ErrInstanceRequired
	}
	if err := validateCommon(c.RedisInstance, c.Concurrency, c.MaxAttempts); err != nil {
		return err
	}
	if c.MaxRetries < 0 {
		return errors.New("max_retries must not be negative")
	}
	return nil
}

// validateCommon covers the fields every consumer shares. The upper bound on
// concurrency is the worker API's: Func takes a uint8, and an instance count
// above 255 is a configuration mistake rather than a plan.
// validateCommon 校验四种消费者共有的字段。并发上限由 worker API 决定：Func
// 接收 uint8，超过 255 的实例数是配置错误，不是计划。
func validateCommon(instance string, concurrency, maxAttempts int) error {
	if strings.TrimSpace(instance) == "" {
		return ErrInstanceRequired
	}
	if concurrency < 1 || concurrency > 255 {
		return fmt.Errorf("concurrency must be between 1 and 255, got %d", concurrency)
	}
	if maxAttempts < 1 {
		return fmt.Errorf("max_attempts must be at least 1, got %d", maxAttempts)
	}
	return nil
}

// Connections is the set of already-decoded broker configurations the
// consumers reuse. The application fills it from the connection blocks it
// already decodes — kafka, amqp, rocketmq, redis — keeping the enabled instances
// only, and each consumer picks its instance by name.
//
// Passing the whole map instead of a single config keeps the resolution — and
// the "instance not enabled" error — at startup, before any consumer goroutine
// exists.
// Connections 是消费者复用的 broker 连接配置。应用用自己已经解码的连接段填充它——
// kafka、amqp、rocketmq、redis——只保留启用的实例，每个消费者按名字挑自己的那个。
//
// 传整张表而不是单个配置，是为了把解析和"实例未启用"的错误留在启动阶段，
// 那时还没有任何消费者 goroutine。
type Connections struct {
	Kafka    map[string]gofykafka.Config
	AMQP     map[string]gofyamqp.Config
	RocketMQ map[string]gofyrocketmq.Config
	Redis    map[string]gofyredis.Config
}

// KafkaConfig resolves one Kafka connection by instance name.
// KafkaConfig 按实例名解析一个 Kafka 连接。
func (c Connections) KafkaConfig(name string) (gofykafka.Config, error) {
	cfg, ok := c.Kafka[name]
	if !ok {
		return gofykafka.Config{}, fmt.Errorf("kafka instance %q is not configured or not enabled", name)
	}
	return cfg, nil
}

// AMQPConfig resolves one AMQP connection by instance name.
// AMQPConfig 按实例名解析一个 AMQP 连接。
func (c Connections) AMQPConfig(name string) (gofyamqp.Config, error) {
	cfg, ok := c.AMQP[name]
	if !ok {
		return gofyamqp.Config{}, fmt.Errorf("amqp instance %q is not configured or not enabled", name)
	}
	return cfg, nil
}

// RocketMQConfig resolves one RocketMQ connection by instance name.
// RocketMQConfig 按实例名解析一个 RocketMQ 连接。
func (c Connections) RocketMQConfig(name string) (gofyrocketmq.Config, error) {
	cfg, ok := c.RocketMQ[name]
	if !ok {
		return gofyrocketmq.Config{}, fmt.Errorf("rocketmq instance %q is not configured or not enabled", name)
	}
	return cfg, nil
}

// RedisConfig resolves one Redis connection by instance name.
// RedisConfig 按实例名解析一个 Redis 连接。
func (c Connections) RedisConfig(name string) (gofyredis.Config, error) {
	cfg, ok := c.Redis[name]
	if !ok {
		return gofyredis.Config{}, fmt.Errorf("redis instance %q is not configured or not enabled", name)
	}
	return cfg, nil
}

// Concurrency converts a validated instance count to the worker API's type.
// Validate has already rejected anything outside 1..255, so the conversion
// cannot silently wrap.
// Concurrency 把校验过的实例数转成 worker API 的类型。Validate 已经拒绝
// 1..255 之外的值，因此转换不会悄悄回绕。
func Concurrency(n int) uint8 {
	if n <= 0 || n > 255 {
		return 1
	}
	return uint8(n)
}

// effectiveMaxAttempts returns the attempt budget actually used, treating an
// unset value as one attempt rather than zero (which would park every message).
// effectiveMaxAttempts 返回实际生效的尝试次数；未设置时按一次处理，而不是零次
// （零次会把每条消息都记为 dead）。
func effectiveMaxAttempts(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
