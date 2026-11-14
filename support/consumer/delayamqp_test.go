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
		TTL:             5 * time.Second,
		DelayExchange:   "demo.orders.delay",
		DelayQueue:      "demo.order.delay.5s",
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
		{"missing ttl", func(c *DelayAMQPConfig) { c.TTL = 0 }},
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
	if cfg.Exchange != "demo.orders.delay" || cfg.Queue != "demo.order.delay.5s" {
		t.Fatalf("staging topology = %s / %s", cfg.Exchange, cfg.Queue)
	}
	if cfg.Delay.Target.Exchange != "demo.orders.delay.ready" ||
		cfg.Delay.Target.Queue != "demo.order.delay.ready" {
		t.Fatalf("target topology = %s / %s", cfg.Delay.Target.Exchange, cfg.Delay.Target.Queue)
	}
	if cfg.Delay.TTL != 5*time.Second {
		t.Fatalf("ttl = %s, want 5s", cfg.Delay.TTL)
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
