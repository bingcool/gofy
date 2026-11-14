package consumer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/queues"
)

func noopDelayQueuesHandler(context.Context, DelayQueuesConfig, DelayQueuesMessage) error {
	return nil
}

func validDelayQueuesConfig() DelayQueuesConfig {
	return DelayQueuesConfig{
		Name:    "order-delay-consumer",
		Key:     "demo:order:delay",
		Handler: noopDelayQueuesHandler,
	}
}

// TestNewDelayQueuesAppliesDefaults pins the delayed-queue defaults. PopLimit
// matters here: unlike the immediate queue, one poll can take a whole batch.
// TestNewDelayQueuesAppliesDefaults 固定延迟队列默认值。PopLimit 在这里尤其重要：
// 与立即队列不同，一次轮询可以取一整批。
func TestNewDelayQueuesAppliesDefaults(t *testing.T) {
	c, err := NewDelayQueues(queuesConns(), validDelayQueuesConfig())
	if err != nil {
		t.Fatalf("NewDelayQueues: %v", err)
	}
	cfg := c.cfg
	if cfg.RedisInstance != "ratelimit" || cfg.Concurrency != 1 {
		t.Fatalf("instance/concurrency = %q/%d", cfg.RedisInstance, cfg.Concurrency)
	}
	if cfg.MaxRetries != 3 || cfg.PopLimit != 10 || cfg.MaxAttempts != 5 {
		t.Fatalf("retries/pop limit/attempts = %d/%d/%d", cfg.MaxRetries, cfg.PopLimit, cfg.MaxAttempts)
	}
	if cfg.IdleDelay != 200*time.Millisecond || cfg.RetryDelay != 3*time.Second {
		t.Fatalf("delays = %v/%v", cfg.IdleDelay, cfg.RetryDelay)
	}
	if c.Concurrency() != 1 || c.Name() != cfg.Name {
		t.Fatalf("worker interface = %q/%d", c.Name(), c.Concurrency())
	}
}

func TestNewDelayQueuesRejectsInvalidConfig(t *testing.T) {
	cases := []struct {
		name string
		edit func(*DelayQueuesConfig)
		want string
	}{
		{"missing name", func(c *DelayQueuesConfig) { c.Name = "" }, "name is required"},
		{"bad name", func(c *DelayQueuesConfig) { c.Name = "not/a/name" }, "invalid worker name"},
		{"missing key", func(c *DelayQueuesConfig) { c.Key = "" }, "key is required"},
		{"missing handler", func(c *DelayQueuesConfig) { c.Handler = nil }, "handler is required"},
		{"bad concurrency", func(c *DelayQueuesConfig) { c.Concurrency = 300 }, "concurrency"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validDelayQueuesConfig()
			tc.edit(&cfg)
			_, err := NewDelayQueues(queuesConns(), cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewDelayQueues = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestNewDelayQueuesRejectsUnknownInstance(t *testing.T) {
	_, err := NewDelayQueues(Connections{}, validDelayQueuesConfig())
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("NewDelayQueues with no connections = %v, want unresolved instance", err)
	}
}

func TestDelayQueuesMessageKeepsSourceAndAttempt(t *testing.T) {
	msg := delayQueuesMessage("demo:order:delay", &queues.Message{
		ID:       "evt-1",
		RetryNum: 2,
		Payload:  []byte(`{"id":"evt-1"}`),
	})
	if msg.Source != "delay_queues" || msg.ID != "evt-1" || msg.Attempt != 3 {
		t.Fatalf("message = %+v", msg)
	}
	if immediate := queuesMessage("demo:order:delay", &queues.Message{ID: "evt-1"}); immediate.Source != "queues" {
		t.Fatalf("immediate queue source changed: %q", immediate.Source)
	}
}
