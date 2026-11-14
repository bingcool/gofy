package consumer

import (
	"context"
	"strings"
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"

	gofyamqp "github.com/bingcool/gofy/support/amqp"
)

func amqpConns() Connections {
	return Connections{
		AMQP: map[string]gofyamqp.Config{
			"default": {Hosts: []gofyamqp.Host{{Host: "127.0.0.1", Port: 5672}}},
		},
	}
}

func noopAMQPHandler(context.Context, AMQPConfig, AMQPMessage) error { return nil }

// TestNewAMQPAppliesDefaults pins the topology defaults: a direct exchange, a
// durable queue and a prefetch window wide enough to keep one consumer busy.
// TestNewAMQPAppliesDefaults 固定 topology 默认值：direct exchange、持久化队列，
// 以及足够喂满一个消费者的 prefetch 窗口。
func TestNewAMQPAppliesDefaults(t *testing.T) {
	consumer, err := NewAMQP(amqpConns(), AMQPConfig{
		Name:       "order-consumer",
		Exchange:   "demo.orders",
		Queue:      "demo.order.events",
		BindingKey: "order.events",
		Handler:    noopAMQPHandler,
	})
	if err != nil {
		t.Fatalf("NewAMQP: %v", err)
	}
	cfg := consumer.cfg
	if cfg.ExchangeType != "direct" {
		t.Fatalf("exchange type = %q, want direct", cfg.ExchangeType)
	}
	if cfg.NonDurable {
		t.Fatal("the queue must be durable by default")
	}
	if cfg.PrefetchCount != 32 {
		t.Fatalf("prefetch = %d, want 32", cfg.PrefetchCount)
	}
	if cfg.ReconnectDelay != 3*time.Second {
		t.Fatalf("reconnect delay = %v", cfg.ReconnectDelay)
	}
	if cfg.MaxAttempts != 5 {
		t.Fatalf("max attempts = %d, want 5", cfg.MaxAttempts)
	}
}

// TestNewAMQPRejects covers the topology rules the broker would otherwise
// reject after the process is already up and holding a pid file.
// TestNewAMQPRejects 覆盖 broker 会拒绝的 topology 规则——那种失败通常发生在进程已经
// 起来、pid 文件已写好之后。
func TestNewAMQPRejects(t *testing.T) {
	valid := AMQPConfig{
		Name:       "order-consumer",
		Exchange:   "demo.orders",
		Queue:      "demo.order.events",
		BindingKey: "order.events",
		Handler:    noopAMQPHandler,
	}
	tests := []struct {
		name   string
		mutate func(*AMQPConfig)
		want   string
	}{
		{name: "no name", mutate: func(c *AMQPConfig) { c.Name = "" }, want: "name is required"},
		{name: "no exchange", mutate: func(c *AMQPConfig) { c.Exchange = "" }, want: "exchange is required"},
		{name: "no queue", mutate: func(c *AMQPConfig) { c.Queue = "" }, want: "queue is required"},
		{name: "no handler", mutate: func(c *AMQPConfig) { c.Handler = nil }, want: "handler is required"},
		{name: "unknown exchange type", mutate: func(c *AMQPConfig) { c.ExchangeType = "stream" }, want: "exchange_type"},
		{name: "direct without keys", mutate: func(c *AMQPConfig) { c.BindingKey = "" }, want: "routing_key or binding_key"},
		{
			name: "direct keys differ",
			mutate: func(c *AMQPConfig) {
				c.BindingKey = "order.a"
				c.RoutingKey = "order.b"
			},
			want: "routing_key == binding_key",
		},
		{name: "unknown instance", mutate: func(c *AMQPConfig) { c.Instance = "order" }, want: `amqp instance "order"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			_, err := NewAMQP(amqpConns(), cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewAMQP = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

// TestAMQPMessageID documents the deliberate empty fallback: without a
// MessageId the message is not deduplicated, which is safer than collapsing two
// different messages that share a routing key.
// TestAMQPMessageID 记录刻意的空兜底：没有 MessageId 的消息不做去重，这比把两条
// 共用 routing key 的不同消息当成同一条更安全。
func TestAMQPMessageID(t *testing.T) {
	if got := amqpMessageID(amqp091.Delivery{}, nil); got != "" {
		t.Fatalf("id = %q, want empty", got)
	}
	if got := amqpMessageID(amqp091.Delivery{MessageId: "msg-1"}, nil); got != "msg-1" {
		t.Fatalf("id = %q, want msg-1", got)
	}
	headers := map[string]string{"X-Correlation-Id": "corr-1"}
	if got := amqpMessageID(amqp091.Delivery{}, headers); got != "corr-1" {
		t.Fatalf("id = %q, want corr-1", got)
	}
}

// TestAMQPMessageMapsRedelivery checks the attempt conversion and that the
// native delivery reaches the handler: both are what a manual Ack needs with
// AutoAck off.
// TestAMQPMessageMapsRedelivery 校验尝试次数的换算，以及原生投递确实传到了 handler：
// 关闭 AutoAck 后手动 Ack 需要的正是这两样。
func TestAMQPMessageMapsRedelivery(t *testing.T) {
	msg := amqpMessage(amqp091.Delivery{
		MessageId:   "msg-1",
		RoutingKey:  "order.events",
		Body:        []byte(`{"type":"order.paid"}`),
		Redelivered: true,
		Headers:     amqp091.Table{"traceparent": "00-abc"},
	})
	if msg.Attempt != 2 {
		t.Fatalf("Attempt = %d, want 2", msg.Attempt)
	}
	if msg.Native == nil {
		t.Fatal("the native delivery was not carried through")
	}
	if _, ok := msg.Native.(amqp091.Delivery); !ok {
		t.Fatalf("Native = %T, want amqp091.Delivery", msg.Native)
	}
	if msg.Headers["traceparent"] != "00-abc" {
		t.Fatal("string headers must survive the flattening")
	}
}
