package consumer

import (
	"context"
	"strings"
	"testing"
	"time"

	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
)

func rocketMQConns() Connections {
	return Connections{
		RocketMQ: map[string]gofyrocketmq.Config{
			"default": {Endpoint: "127.0.0.1:8081"},
		},
	}
}

func noopRocketMQHandler(context.Context, RocketMQConfig, RocketMQMessage) error { return nil }

// TestNewRocketMQAppliesDefaults pins the pull defaults: one worker instance,
// one SDK pull worker, a batch the broker can fill in one round trip. It also
// pins the tag trim: the SDK joins Tags verbatim, so a padded tag would never
// match.
// TestNewRocketMQAppliesDefaults 固定拉取默认值：一个 worker 实例、一个 SDK 拉取
// worker，以及 broker 一次往返就能填满的批量。同时固定 tag 的裁剪：SDK 原样拼接
// Tags，带空格的 tag 永远匹配不上。
func TestNewRocketMQAppliesDefaults(t *testing.T) {
	consumer, err := NewRocketMQ(rocketMQConns(), RocketMQConfig{
		Name:    "order-consumer",
		Topic:   "demo-order-events",
		Group:   "order-group",
		Tags:    []string{" order ", "paid"},
		Handler: noopRocketMQHandler,
	})
	if err != nil {
		t.Fatalf("NewRocketMQ: %v", err)
	}
	cfg := consumer.cfg
	if cfg.Concurrency != 1 || cfg.PullWorkers != 1 {
		t.Fatalf("concurrency/pull workers = %d/%d, want 1/1", cfg.Concurrency, cfg.PullWorkers)
	}
	if cfg.BatchSize != 16 {
		t.Fatalf("batch size = %d, want 16", cfg.BatchSize)
	}
	if cfg.AwaitDuration != 5*time.Second || cfg.InvisibleDuration != 30*time.Second {
		t.Fatalf("durations = %v/%v", cfg.AwaitDuration, cfg.InvisibleDuration)
	}
	if got := strings.Join(cfg.Tags, ","); got != "order,paid" {
		t.Fatalf("tags = %q, want order,paid", got)
	}
}

// TestNewRocketMQRejects covers the required subscription fields.
// TestNewRocketMQRejects 覆盖订阅的必填字段。
func TestNewRocketMQRejects(t *testing.T) {
	valid := RocketMQConfig{
		Name:    "order-consumer",
		Topic:   "demo-order-events",
		Group:   "order-group",
		Handler: noopRocketMQHandler,
	}
	tests := []struct {
		name   string
		mutate func(*RocketMQConfig)
		want   string
	}{
		{name: "no name", mutate: func(c *RocketMQConfig) { c.Name = "" }, want: "name is required"},
		{name: "no topic", mutate: func(c *RocketMQConfig) { c.Topic = "" }, want: "topic is required"},
		{name: "no group", mutate: func(c *RocketMQConfig) { c.Group = "" }, want: "group is required"},
		{name: "no handler", mutate: func(c *RocketMQConfig) { c.Handler = nil }, want: "handler is required"},
		{name: "empty tag", mutate: func(c *RocketMQConfig) { c.Tags = []string{"order", " "} }, want: `invalid tag ""`},
		{name: "tag with separator", mutate: func(c *RocketMQConfig) { c.Tags = []string{"order||paid"} }, want: "invalid tag"},
		{name: "unknown instance", mutate: func(c *RocketMQConfig) { c.Instance = "order" }, want: `rocketmq instance "order"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			_, err := NewRocketMQ(rocketMQConns(), cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewRocketMQ = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

// TestRocketMQMessageID prefers the broker id over the offset.
// TestRocketMQMessageID 优先使用 broker 的消息 ID，而不是位点。
func TestRocketMQMessageID(t *testing.T) {
	if got := rocketmqMessageID(&gofyrocketmq.Message{MessageID: "A1", Topic: "t", Offset: 3}); got != "A1" {
		t.Fatalf("id = %q, want A1", got)
	}
	if got := rocketmqMessageID(&gofyrocketmq.Message{Topic: "t", Offset: 3}); got != "t@3" {
		t.Fatalf("id = %q, want t@3", got)
	}
}

// TestRocketMQMessageKeepsNative checks the conversion keeps the broker's own
// delivery counter and the native message, which is what a handler logs from.
// TestRocketMQMessageKeepsNative 校验转换保留 broker 自己的投递计数与原生消息——那正是
// handler 记日志时用的。
func TestRocketMQMessageKeepsNative(t *testing.T) {
	native := &gofyrocketmq.Message{
		MessageID:       "A1",
		Topic:           "t",
		Body:            []byte(`{"type":"order.paid"}`),
		DeliveryAttempt: 3,
		Keys:            []string{"order-1", "user-7"},
	}
	msg := rocketmqMessage(native)
	if msg.Attempt != 3 {
		t.Fatalf("Attempt = %d, want 3", msg.Attempt)
	}
	if msg.Key != "order-1,user-7" {
		t.Fatalf("Key = %q", msg.Key)
	}
	if msg.Native != native {
		t.Fatal("the native message was not carried through")
	}
}
