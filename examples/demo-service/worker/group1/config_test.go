package group1

import (
	"strings"
	"testing"
	"time"
)

// TestConfigValidateIgnoresDisabledBlocks pins the shipped config: every
// consumer is listed with enabled: false, and that must validate without
// demanding a topic nobody is going to use.
// TestConfigValidateIgnoresDisabledBlocks 固定仓库里的配置：四个消费者都以
// enabled: false 列出，这必须能通过校验，而不是要求填一个没人会用的 topic。
func TestConfigValidateIgnoresDisabledBlocks(t *testing.T) {
	cfg := Config{Consumers: Consumers{
		Kafka:    KafkaConsumer{Instance: "default", Concurrency: 1, MaxAttempts: 5},
		AMQP:     AMQPConsumer{Instance: "default", Concurrency: 1, MaxAttempts: 5},
		RocketMQ: RocketMQConsumer{Instance: "default", Concurrency: 1, MaxAttempts: 5},
		Queues:   QueuesConsumer{RedisInstance: "ratelimit", Concurrency: 2, MaxAttempts: 5},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate = %v, want nil", err)
	}
}

// TestConfigValidateKafka covers the three mistakes that are cheap to catch at
// startup and expensive to diagnose at runtime: no topic, no group, and an
// instance count the worker API cannot hold.
// TestConfigValidateKafka 覆盖三个在启动时容易发现、运行时难以诊断的错误：缺
// topic、缺消费组，以及 worker API 装不下的实例数。
func TestConfigValidateKafka(t *testing.T) {
	valid := KafkaConsumer{
		Enabled:     true,
		Instance:    "default",
		Concurrency: 1,
		Topic:       "demo.order.events",
		Group:       "demo-service-order-consumer",
		MaxAttempts: 5,
	}
	tests := []struct {
		name   string
		mutate func(*KafkaConsumer)
		want   string
	}{
		{name: "valid", mutate: func(*KafkaConsumer) {}},
		{name: "missing topic", mutate: func(c *KafkaConsumer) { c.Topic = " " }, want: "topic is required"},
		{name: "missing group", mutate: func(c *KafkaConsumer) { c.Group = "" }, want: "group is required"},
		{name: "zero concurrency", mutate: func(c *KafkaConsumer) { c.Concurrency = 0 }, want: "concurrency must be between 1 and 255"},
		{name: "too many instances", mutate: func(c *KafkaConsumer) { c.Concurrency = 256 }, want: "concurrency must be between 1 and 255"},
		{name: "bad commit mode", mutate: func(c *KafkaConsumer) { c.CommitMode = "batch" }, want: "commit_mode"},
		{name: "bad offset reset", mutate: func(c *KafkaConsumer) { c.AutoOffsetReset = "middle" }, want: "auto_offset_reset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			consumer := valid
			tt.mutate(&consumer)
			cfg := Config{Consumers: Consumers{Kafka: consumer}}
			err := cfg.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), "worker.consumers.kafka") {
				t.Fatalf("Validate = %v, want the config path in the message", err)
			}
		})
	}
}

// TestConfigValidateAMQPTopology checks the topology rules the AMQP layer
// enforces, because a queue that refuses to declare fails after the process is
// already up and holding a pid file.
// TestConfigValidateAMQPTopology 校验 AMQP 层的 topology 规则：一个声明不出来的
// 队列会在进程已经起来、pid 文件已写好之后才失败。
func TestConfigValidateAMQPTopology(t *testing.T) {
	valid := AMQPConsumer{
		Enabled:      true,
		Instance:     "default",
		Concurrency:  1,
		Exchange:     "demo.orders",
		ExchangeType: "direct",
		Queue:        "demo.order.events",
		RoutingKey:   "order.events",
		BindingKey:   "order.events",
		MaxAttempts:  5,
	}
	tests := []struct {
		name   string
		mutate func(*AMQPConsumer)
		want   string
	}{
		{name: "valid", mutate: func(*AMQPConsumer) {}},
		{name: "missing exchange", mutate: func(c *AMQPConsumer) { c.Exchange = "" }, want: "exchange is required"},
		{name: "missing queue", mutate: func(c *AMQPConsumer) { c.Queue = "" }, want: "queue is required"},
		{name: "direct keys differ", mutate: func(c *AMQPConsumer) { c.BindingKey = "other" }, want: "routing_key == binding_key"},
		{name: "direct without keys", mutate: func(c *AMQPConsumer) { c.RoutingKey = ""; c.BindingKey = "" }, want: "routing_key or binding_key"},
		{name: "topic keys may differ", mutate: func(c *AMQPConsumer) { c.ExchangeType = "topic"; c.BindingKey = "order.*" }},
		{name: "unknown exchange type", mutate: func(c *AMQPConsumer) { c.ExchangeType = "stream" }, want: "exchange_type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			consumer := valid
			tt.mutate(&consumer)
			cfg := Config{Consumers: Consumers{AMQP: consumer}}
			err := cfg.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

// TestConfigValidateQueues checks the two required fields of the Redis queue
// consumer.
// TestConfigValidateQueues 校验 Redis 队列消费者的两个必填字段。
func TestConfigValidateQueues(t *testing.T) {
	valid := QueuesConsumer{
		Enabled:       true,
		RedisInstance: "ratelimit",
		Concurrency:   2,
		Key:           "demo:order:events",
		MaxAttempts:   5,
	}
	tests := []struct {
		name   string
		mutate func(*QueuesConsumer)
		want   string
	}{
		{name: "valid", mutate: func(*QueuesConsumer) {}},
		{name: "missing key", mutate: func(c *QueuesConsumer) { c.Key = "" }, want: "key is required"},
		{name: "missing redis instance", mutate: func(c *QueuesConsumer) { c.RedisInstance = "" }, want: "instance is required"},
		{name: "zero attempts", mutate: func(c *QueuesConsumer) { c.MaxAttempts = 0 }, want: "max_attempts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			consumer := valid
			tt.mutate(&consumer)
			cfg := Config{Consumers: Consumers{Queues: consumer}}
			err := cfg.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

// TestConcurrencyConversion pins the guard around the uint8 conversion: values
// outside the range fall back to one instance instead of wrapping to 0 (which
// the group constructor would reject) or to an arbitrary number.
// TestConcurrencyConversion 固定 uint8 转换的兜底行为：超范围的值回落到 1 个
// 实例，而不是回绕成 0（会被分组构造函数拒绝）或某个任意数字。
func TestConcurrencyConversion(t *testing.T) {
	tests := map[int]uint8{-1: 1, 0: 1, 1: 1, 4: 4, 255: 255, 256: 1}
	for in, want := range tests {
		if got := concurrency(in); got != want {
			t.Fatalf("concurrency(%d) = %d, want %d", in, got, want)
		}
	}
}

// TestMaxAttemptsFloor keeps an unset attempt budget at one attempt: zero would
// park every message on its first delivery.
// TestMaxAttemptsFloor 让未设置的尝试次数保持为一次：零次会把每条消息在第一次
// 投递时就标记为 dead。
func TestMaxAttemptsFloor(t *testing.T) {
	if got := effectiveMaxAttempts(0); got != 1 {
		t.Fatalf("effectiveMaxAttempts(0) = %d, want 1", got)
	}
	if got := effectiveMaxAttempts(5); got != 5 {
		t.Fatalf("effectiveMaxAttempts(5) = %d, want 5", got)
	}
}

// TestNegativeRetryDelayFallsBackToDefaults records a deliberate omission: a
// negative duration is not rejected here because every consumer layer clamps a
// non-positive delay to its own default, so the effective behaviour is still
// sane. Rejecting it would only add a second place to keep in sync.
// TestNegativeRetryDelayFallsBackToDefaults 记录一处刻意的省略：负的时长不在这里
// 拒绝，因为每个消费层都会把非正延迟回落到自己的默认值，实际行为仍然正确。在这里
// 拒绝只会多一个需要同步维护的地方。
func TestNegativeRetryDelayFallsBackToDefaults(t *testing.T) {
	cfg := Config{Consumers: Consumers{Kafka: KafkaConsumer{
		Enabled:     true,
		Instance:    "default",
		Concurrency: 1,
		Topic:       "demo.order.events",
		Group:       "demo-service-order-consumer",
		RetryDelay:  -1 * time.Second,
		MaxAttempts: 5,
	}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate = %v, want nil (the consumer layer applies the default)", err)
	}
}
