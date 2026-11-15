package consumer

import (
	"context"
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

func noopDelayAMQPHandler(context.Context, DelayAMQPConfig, DelayAMQPMessage) error { return nil }

func delayAMQPSubscription() DelayAMQPConfig {
	return DelayAMQPConfig{
		Name:            "order-amqp-delay-consumer",
		Instance:        "default",
		DelayExchange:   "demo.orders.delay",
		DelayQueue:      "demo.order.delay.staging",
		DelayBindingKey: "order.delayed",
		DelayRoutingKey: "order.delayed",
		Exchange:        "demo.orders.delay.ready",
		Queue:           "demo.order.delay.ready",
		BindingKey:      "order.delayed",
		RoutingKey:      "order.delayed",
		Idempotency:     true,
		Handler:         noopDelayAMQPHandler,
	}
}

// TestNewDelayAMQPAppliesDefaults pins the same defaults as the immediate AMQP
// subscription — a reader should not have to learn two sets of them.
// TestNewDelayAMQPAppliesDefaults 固定与立即 AMQP 订阅一致的默认值，读者不该记两套。
func TestNewDelayAMQPAppliesDefaults(t *testing.T) {
	cfg := delayAMQPSubscription()
	cfg.Instance = ""
	cfg.DelayExchangeType = ""
	cfg.ExchangeType = ""
	consumer, err := NewDelayAMQP(amqpConns(), cfg)
	if err != nil {
		t.Fatalf("NewDelayAMQP: %v", err)
	}
	got := consumer.cfg
	if got.Instance != "default" {
		t.Fatalf("instance = %q, want default", got.Instance)
	}
	if got.Concurrency != 1 || got.MaxAttempts != 5 {
		t.Fatalf("concurrency = %d, max attempts = %d, want 1 / 5", got.Concurrency, got.MaxAttempts)
	}
	if got.DelayExchangeType != "direct" || got.ExchangeType != "direct" {
		t.Fatalf("exchange types = %q / %q, want direct / direct", got.DelayExchangeType, got.ExchangeType)
	}
	if got.PrefetchCount != 32 || got.ReconnectDelay != 3*time.Second {
		t.Fatalf("prefetch = %d, reconnect = %s, want 32 / 3s", got.PrefetchCount, got.ReconnectDelay)
	}
}

// TestNewDelayAMQPRejectsInvalidConfig keeps a broken declaration out of the
// group. Both halves of the topology must be declared, and a direct exchange
// needs one key because the dead-letter routing key comes from the target's.
// TestNewDelayAMQPRejectsInvalidConfig 让错误的声明进不了分组。两半拓扑都必须声明，
// direct exchange 还必须有 key——死信 routing key 正是取目标拓扑的。
func TestNewDelayAMQPRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DelayAMQPConfig)
	}{
		{"missing name", func(c *DelayAMQPConfig) { c.Name = "" }},
		{"invalid name", func(c *DelayAMQPConfig) { c.Name = "not a name" }},
		{"missing handler", func(c *DelayAMQPConfig) { c.Handler = nil }},
		{"invalid concurrency", func(c *DelayAMQPConfig) { c.Concurrency = -1 }},
		{"negative ttl", func(c *DelayAMQPConfig) { c.TTL = -time.Second }},
		{"sub-millisecond ttl", func(c *DelayAMQPConfig) { c.TTL = time.Microsecond }},
		{"negative prefetch", func(c *DelayAMQPConfig) { c.PrefetchCount = -1 }},
		{"missing delay exchange", func(c *DelayAMQPConfig) { c.DelayExchange = "" }},
		{"missing delay queue", func(c *DelayAMQPConfig) { c.DelayQueue = "" }},
		{"missing target exchange", func(c *DelayAMQPConfig) { c.Exchange = "" }},
		{"missing target queue", func(c *DelayAMQPConfig) { c.Queue = "" }},
		{"delay direct without keys", func(c *DelayAMQPConfig) {
			c.DelayBindingKey, c.DelayRoutingKey = "", ""
		}},
		{"target direct without keys", func(c *DelayAMQPConfig) {
			c.BindingKey, c.RoutingKey = "", ""
		}},
		{"direct key mismatch", func(c *DelayAMQPConfig) { c.RoutingKey = "other" }},
		{"invalid exchange type", func(c *DelayAMQPConfig) { c.ExchangeType = "consistent_hash" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := delayAMQPSubscription()
			tt.mutate(&cfg)
			if _, err := NewDelayAMQP(amqpConns(), cfg); err == nil {
				t.Fatal("NewDelayAMQP accepted an invalid config")
			}
		})
	}
}

// TestDelayAMQPQueueConfigDescribesBothHalves pins the single source of truth
// a producer publishes through. If these fields drift, the producer would
// declare a different staging queue than the one the consumer waits on.
// TestDelayAMQPQueueConfigDescribesBothHalves 固定生产端使用的唯一配置来源。字段一旦
// 错位，生产端声明的暂存队列就会与消费端等待的队列不同。
func TestDelayAMQPQueueConfigDescribesBothHalves(t *testing.T) {
	consumer, err := NewDelayAMQP(amqpConns(), delayAMQPSubscription())
	if err != nil {
		t.Fatalf("NewDelayAMQP: %v", err)
	}
	cfg := consumer.cfg.QueueConfig()
	if cfg.Delay == nil {
		t.Fatal("QueueConfig has no Delay workflow")
	}
	if cfg.Exchange != "demo.orders.delay" || cfg.Queue != "demo.order.delay.staging" {
		t.Fatalf("staging topology = %s / %s", cfg.Exchange, cfg.Queue)
	}
	if cfg.Delay.Target.Exchange != "demo.orders.delay.ready" ||
		cfg.Delay.Target.Queue != "demo.order.delay.ready" {
		t.Fatalf("target topology = %s / %s", cfg.Delay.Target.Exchange, cfg.Delay.Target.Queue)
	}
	// The default is the publish-time-only mode: the subscription owns the
	// topology, the producer owns the delay, and no queue TTL is declared.
	// 默认是纯发布端模式：订阅只拥有拓扑，延迟归生产端，队列不声明 TTL。
	if cfg.Delay.TTL != 0 {
		t.Fatalf("ttl = %s, want 0 (publish-time-only)", cfg.Delay.TTL)
	}
	if !cfg.Durable || !cfg.Delay.Target.Durable {
		t.Fatal("both queues must be durable by default")
	}
	// The transport validates the same declaration Run will use.
	consumer.cfg.NonDurable = true
	if consumer.cfg.QueueConfig().Durable {
		t.Fatal("NonDurable must propagate to both halves")
	}
}

// TestDelayAMQPPassiveReachesBothHalves pins the flag a foreign topology needs.
// A plain declare compares arguments with the broker, so a staging queue that
// a Java service created with x-message-ttl would fail with
// PRECONDITION_FAILED. Both halves must switch to passive together: leaving
// the target on a normal declare would redeclare half the workflow.
// TestDelayAMQPPassiveReachesBothHalves 固定跨语言拓扑必需的开关。普通 declare 会
// 与 broker 比对参数，Java 服务建过带 x-message-ttl 的暂存队列时会
// PRECONDITION_FAILED。两半必须一起切成 passive，否则目标那半仍会重新声明。
func TestDelayAMQPPassiveReachesBothHalves(t *testing.T) {
	cfg := delayAMQPSubscription()
	if cfg.QueueConfig().Passive || cfg.QueueConfig().Delay.Target.Passive {
		t.Fatal("passive must stay off by default")
	}

	cfg.Passive = true
	consumer, err := NewDelayAMQP(amqpConns(), cfg)
	if err != nil {
		t.Fatalf("NewDelayAMQP with passive: %v", err)
	}
	got := consumer.cfg.QueueConfig()
	if !got.Passive || !got.Delay.Target.Passive {
		t.Fatalf("passive staging=%v target=%v, want true / true", got.Passive, got.Delay.Target.Passive)
	}
	// Passive changes how the declarations are checked, not what is published
	// or consumed. Names and keys must still be the ones the other process used.
	// Passive 只改变声明的检查方式，不改变发布与消费的地址；名称和 key 仍须与对方一致。
	if got.Exchange != "demo.orders.delay" || got.Queue != "demo.order.delay.staging" ||
		got.Delay.Target.Queue != "demo.order.delay.ready" || got.RoutingKey != "order.delayed" {
		t.Fatalf("passive changed the topology: %+v", got)
	}
	if got.Delay.TTL != 0 {
		t.Fatalf("ttl = %s, want 0 (publish-time-only)", got.Delay.TTL)
	}
}

// TestDelayAMQPPassiveWorksWithTopicAndTransientQueues keeps the flag
// independent of the other topology switches rather than a special case of
// the direct subscription.
// TestDelayAMQPPassiveWorksWithTopicAndTransientQueues 确认这个开关与 topic、临时队列等
// 其它拓扑选项互不干扰，不是 direct 订阅的特例。
func TestDelayAMQPPassiveWorksWithTopicAndTransientQueues(t *testing.T) {
	cfg := delayAMQPTopicSubscription()
	cfg.Passive = true
	cfg.NonDurable = true
	consumer, err := NewDelayAMQP(amqpConns(), cfg)
	if err != nil {
		t.Fatalf("NewDelayAMQP with passive + topic: %v", err)
	}
	got := consumer.cfg.QueueConfig()
	if !got.Passive || !got.Delay.Target.Passive {
		t.Fatal("passive must reach a topic topology as well")
	}
	if !got.Delay.PreserveRoutingKey || got.ExchangeType != "topic" {
		t.Fatal("passive must not clear the topic routing-key flag or the exchange type")
	}
	if got.Durable || got.Delay.Target.Durable {
		t.Fatal("NonDurable must still propagate to both halves")
	}
}

// TestDelayAMQPFixedDelayModeStillWorks keeps the classic queue-level TTL
// available for subscriptions already deployed with one. Zero is the default,
// not the only mode.
// TestDelayAMQPFixedDelayModeStillWorks 保留经典队列级 TTL，给已经带 TTL 部署的
// 订阅留一条路。零值是默认，不是唯一模式。
func TestDelayAMQPFixedDelayModeStillWorks(t *testing.T) {
	cfg := delayAMQPSubscription()
	cfg.TTL = 10 * time.Second
	consumer, err := NewDelayAMQP(amqpConns(), cfg)
	if err != nil {
		t.Fatalf("NewDelayAMQP with a queue ttl: %v", err)
	}
	if got := consumer.cfg.QueueConfig().Delay.TTL; got != 10*time.Second {
		t.Fatalf("ttl = %s, want 10s", got)
	}
}

func delayAMQPTopicSubscription() DelayAMQPConfig {
	cfg := delayAMQPSubscription()
	cfg.Name = "order-amqp-delay-topic-consumer"
	cfg.PreserveRoutingKey = true
	cfg.DelayExchange = "demo.orders.delay.topic"
	cfg.DelayExchangeType = "topic"
	cfg.DelayQueue = "demo.order.delay.topic.staging"
	cfg.DelayBindingKey = "order.*"
	cfg.DelayRoutingKey = ""
	cfg.Exchange = "demo.orders.delay.topic.ready"
	cfg.ExchangeType = "topic"
	cfg.Queue = "demo.order.delay.topic.ready"
	cfg.BindingKey = "order.*"
	cfg.RoutingKey = ""
	return cfg
}

// TestDelayAMQPTopicPreservesRoutingKey pins the option a topic target needs.
// Without it the broker would dead-letter every message under one fixed key
// and the topic binding would no longer select anything.
// TestDelayAMQPTopicPreservesRoutingKey 固定 topic 目标必需的开关。不开时 broker 会把
// 每条消息都用同一个固定 key 死信，topic 绑定也就失去了筛选作用。
func TestDelayAMQPTopicPreservesRoutingKey(t *testing.T) {
	consumer, err := NewDelayAMQP(amqpConns(), delayAMQPTopicSubscription())
	if err != nil {
		t.Fatalf("NewDelayAMQP with a topic topology: %v", err)
	}
	cfg := consumer.cfg.QueueConfig()
	if cfg.Delay == nil || !cfg.Delay.PreserveRoutingKey {
		t.Fatal("the topic subscription must preserve the original routing key")
	}
	if cfg.ExchangeType != "topic" || cfg.Delay.Target.ExchangeType != "topic" {
		t.Fatalf("exchange types = %q / %q, want topic / topic", cfg.ExchangeType, cfg.Delay.Target.ExchangeType)
	}
	if cfg.BindingKey != "order.*" || cfg.Delay.Target.BindingKey != "order.*" {
		t.Fatalf("bindings = %q / %q, want order.*", cfg.BindingKey, cfg.Delay.Target.BindingKey)
	}
	if delayAMQPSubscription().QueueConfig().Delay.PreserveRoutingKey {
		t.Fatal("the default must stay false for existing direct subscriptions")
	}
}

// TestNewDelayAMQPRejectsTopicWithoutKey guards against a topic binding that
// matches nothing: an empty key would leave the queue permanently idle.
// TestNewDelayAMQPRejectsTopicWithoutKey 防止空 binding key 让 topic 队列永远收不到消息。
func TestNewDelayAMQPRejectsTopicWithoutKey(t *testing.T) {
	for name, mutate := range map[string]func(*DelayAMQPConfig){
		"staging": func(c *DelayAMQPConfig) { c.DelayBindingKey = "" },
		"target":  func(c *DelayAMQPConfig) { c.BindingKey = "" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := delayAMQPTopicSubscription()
			mutate(&cfg)
			if _, err := NewDelayAMQP(amqpConns(), cfg); err == nil {
				t.Fatal("NewDelayAMQP accepted a topic topology without a binding key")
			}
		})
	}
}

// TestDelayAMQPMessageHasItsOwnSource keeps delayed deliveries out of the
// immediate subscription's log and trace bucket.
// TestDelayAMQPMessageHasItsOwnSource 让延迟投递不会混进立即订阅的日志与 trace 分类。
func TestDelayAMQPMessageHasItsOwnSource(t *testing.T) {
	message := delayAMQPMessage(amqp091.Delivery{
		MessageId:  "evt-1",
		RoutingKey: "order.delayed",
		Body:       []byte(`{"id":"evt-1"}`),
	})
	if message.Source != "amqp_delay" {
		t.Fatalf("source = %q, want amqp_delay", message.Source)
	}
	if message.ID != "evt-1" {
		t.Fatalf("id = %q, want evt-1", message.ID)
	}
}
