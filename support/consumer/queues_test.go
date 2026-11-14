package consumer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/queues"
	gofyredis "github.com/bingcool/gofy/support/redis"
)

func queuesConns() Connections {
	return Connections{
		Redis: map[string]gofyredis.Config{
			"ratelimit": {Addr: "127.0.0.1:6379"},
		},
	}
}

func noopQueuesHandler(context.Context, QueuesMessage) error { return nil }

// TestNewQueuesAppliesDefaults pins the queue defaults: one pop at a time, a
// short idle wait and a retry that does not spin.
// TestNewQueuesAppliesDefaults 固定队列默认值：一次一个弹出、短暂空转等待，以及不会
// 空转的重试间隔。
func TestNewQueuesAppliesDefaults(t *testing.T) {
	consumer, err := NewQueues(queuesConns(), QueuesConfig{
		Name:    "order-consumer",
		Key:     "demo:order:events",
		Handler: noopQueuesHandler,
	})
	if err != nil {
		t.Fatalf("NewQueues: %v", err)
	}
	cfg := consumer.cfg
	if cfg.RedisInstance != "ratelimit" {
		t.Fatalf("redis instance = %q, want ratelimit", cfg.RedisInstance)
	}
	if cfg.Concurrency != 1 {
		t.Fatalf("concurrency = %d, want 1", cfg.Concurrency)
	}
	if cfg.MaxRetries != 3 {
		t.Fatalf("max retries = %d, want 3", cfg.MaxRetries)
	}
	if cfg.IdleDelay != 200*time.Millisecond || cfg.RetryDelay != 3*time.Second {
		t.Fatalf("delays = %v/%v", cfg.IdleDelay, cfg.RetryDelay)
	}
}

// TestNewQueuesRejects covers the required fields: without a key there is no
// queue, and without a handler the pop would drop messages silently.
// TestNewQueuesRejects 覆盖必填字段：没有 key 就没有队列；没有 handler，弹出即丢消息。
func TestNewQueuesRejects(t *testing.T) {
	valid := QueuesConfig{
		Name:    "order-consumer",
		Key:     "demo:order:events",
		Handler: noopQueuesHandler,
	}
	tests := []struct {
		name   string
		mutate func(*QueuesConfig)
		want   string
	}{
		{name: "no name", mutate: func(c *QueuesConfig) { c.Name = "" }, want: "name is required"},
		{name: "no key", mutate: func(c *QueuesConfig) { c.Key = "" }, want: "key is required"},
		{name: "no handler", mutate: func(c *QueuesConfig) { c.Handler = nil }, want: "handler is required"},
		{name: "unknown instance", mutate: func(c *QueuesConfig) { c.RedisInstance = "order" }, want: `redis instance "order"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			_, err := NewQueues(queuesConns(), cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewQueues = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

// TestQueuesMessageMapsRetryCount checks the attempt conversion: RetryNum is
// zero-based because the queue counts retries, while Attempt is one-based
// because the ledger counts deliveries.
// TestQueuesMessageMapsRetryCount 校验尝试次数的换算：RetryNum 从 0 开始（队列
// 计的是重试次数），Attempt 从 1 开始（台账计的是投递次数）。
func TestQueuesMessageMapsRetryCount(t *testing.T) {
	native := &queues.Message{
		ID:        "q-1",
		RetryNum:  2,
		Timestamp: 1_700_000_000,
		Payload:   []byte(`{"type":"order.paid","payload":{"order_id":1}}`),
	}
	msg := queuesMessage("demo:order", native)
	if msg.Attempt != 3 {
		t.Fatalf("Attempt = %d, want 3", msg.Attempt)
	}
	if msg.ID != "q-1" {
		t.Fatalf("ID = %q, want q-1", msg.ID)
	}
	if msg.Enqueued.Unix() != 1_700_000_000 {
		t.Fatalf("Enqueued = %v", msg.Enqueued)
	}
	if msg.Native != native {
		t.Fatal("the native entry was not carried through")
	}
	if _, err := DecodeEnvelope(msg); err != nil {
		t.Fatalf("the queue payload must decode as an envelope: %v", err)
	}
}
