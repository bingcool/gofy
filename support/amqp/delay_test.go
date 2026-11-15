package amqp

import (
	"context"
	"errors"
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

func delayQueueConfig(ttl time.Duration) QueueConfig {
	return QueueConfig{
		Topology: Topology{
			Exchange:   "orders.delay",
			Queue:      "orders.delay",
			RoutingKey: "delayed",
		},
		Delay: &DelayConfig{
			TTL: ttl,
			Target: Topology{
				Exchange:   "orders",
				Queue:      "order.ready",
				RoutingKey: "delayed",
			},
		},
	}
}

// TestMessageExpiration pins the boundary between the low-level TTL and the
// relative delay. A delay that the staging queue cannot honour must fail at
// the call site, not turn into a shorter wait or a vanished message on the
// broker.
// TestMessageExpiration 固定底层 TTL 与相对延迟之间的边界。暂存队列无法满足的延迟
// 必须在调用点失败，而不是在 broker 上变成更短的等待或一条消失的消息。
func TestMessageExpiration(t *testing.T) {
	delayed := delayQueueConfig(5 * time.Second)
	plain := delayQueueConfig(5 * time.Second)
	plain.Delay = nil

	tests := []struct {
		name string
		cfg  QueueConfig
		opts PublishOptions
		want time.Duration
		err  error
	}{
		{
			name: "no delay keeps the raw expiration",
			cfg:  delayed,
			opts: PublishOptions{Expiration: 2 * time.Second},
			want: 2 * time.Second,
		},
		{
			name: "no delay and no expiration",
			cfg:  delayed,
			opts: PublishOptions{},
			want: 0,
		},
		{
			name: "delay becomes the message ttl",
			cfg:  delayed,
			opts: PublishOptions{Delay: 3 * time.Second},
			want: 3 * time.Second,
		},
		{
			name: "delay equal to the queue ttl is allowed",
			cfg:  delayed,
			opts: PublishOptions{Delay: 5 * time.Second},
			want: 5 * time.Second,
		},
		{
			name: "delay and expiration are mutually exclusive",
			cfg:  delayed,
			opts: PublishOptions{Delay: time.Second, Expiration: time.Second},
			err:  errors.New("amqp: delay and expiration are mutually exclusive"),
		},
		{
			name: "sub-millisecond delay is rejected",
			cfg:  delayed,
			opts: PublishOptions{Delay: time.Microsecond},
			err:  ErrInvalidDelay,
		},
		{
			name: "negative delay is rejected",
			cfg:  delayed,
			opts: PublishOptions{Delay: -time.Second},
			err:  ErrInvalidDelay,
		},
		{
			name: "delay past the queue ttl is rejected instead of clamped",
			cfg:  delayed,
			opts: PublishOptions{Delay: 6 * time.Second},
			err:  ErrInvalidDelay,
		},
		{
			name: "delay on a plain queue would drop the message",
			cfg:  plain,
			opts: PublishOptions{Delay: time.Second},
			err:  ErrDelayRequiresWorkflow,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := messageExpiration(tt.cfg, tt.opts)
			if tt.err != nil {
				if err == nil {
					t.Fatalf("messageExpiration() = %s, want error %v", got, tt.err)
				}
				if !errors.Is(err, tt.err) && err.Error() != tt.err.Error() {
					t.Fatalf("error = %v, want %v", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("messageExpiration: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expiration = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestPublishRejectsInvalidDelayBeforeConnecting makes sure the guard runs
// before Publish touches the channel, so a bad delay fails even when the
// broker is unreachable.
// TestPublishRejectsInvalidDelayBeforeConnecting 确认校验发生在 Publish 碰 channel
// 之前，broker 不可达时坏延迟也会立刻失败。
func TestPublishRejectsInvalidDelayBeforeConnecting(t *testing.T) {
	q, err := NewQueue(&Client{}, delayQueueConfig(5*time.Second))
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	err = q.Publish(context.Background(), []byte("later"), PublishOptions{Delay: 10 * time.Second})
	if !errors.Is(err, ErrInvalidDelay) {
		t.Fatalf("Publish error = %v, want ErrInvalidDelay", err)
	}
}

// TestDelaySourceArgsPreservesRoutingKey pins the topic path. With a topic
// target the broker must dead-letter with the message's own routing key, so no
// x-dead-letter-routing-key may reach QueueDeclare at all — including one the
// caller put in Arguments by hand.
// TestDelaySourceArgsPreservesRoutingKey 固定 topic 路径。目标是 topic exchange 时，
// broker 必须按消息原始的 routing key 死信，因此 QueueDeclare 不能收到
// x-dead-letter-routing-key —— 连调用方手工写进 Arguments 的那一个也不能留。
func TestDelaySourceArgsPreservesRoutingKey(t *testing.T) {
	cfg := delayQueueConfig(5 * time.Second)
	args := delaySourceArgs(cfg)
	if args["x-dead-letter-exchange"] != "orders" {
		t.Fatalf("dead-letter exchange = %v, want orders", args["x-dead-letter-exchange"])
	}
	if args["x-dead-letter-routing-key"] != "delayed" {
		t.Fatalf("default dead-letter key = %v, want delayed", args["x-dead-letter-routing-key"])
	}
	if args["x-message-ttl"] != int64(5000) {
		t.Fatalf("ttl = %v, want 5000ms", args["x-message-ttl"])
	}

	cfg.Delay.PreserveRoutingKey = true
	args = delaySourceArgs(cfg)
	if _, ok := args["x-dead-letter-routing-key"]; ok {
		t.Fatalf("preserved key still declares a dead-letter routing key: %v", args)
	}
	if args["x-dead-letter-exchange"] != "orders" || args["x-message-ttl"] != int64(5000) {
		t.Fatalf("preserving the key dropped the other delay arguments: %v", args)
	}

	// A caller-supplied value must not sneak the override back in.
	cfg.Topology.Arguments = amqp091.Table{
		"x-dead-letter-routing-key": "manual",
		"x-max-length":              int64(100),
	}
	args = delaySourceArgs(cfg)
	if _, ok := args["x-dead-letter-routing-key"]; ok {
		t.Fatalf("a manual dead-letter routing key survived: %v", args)
	}
	if args["x-max-length"] != int64(100) {
		t.Fatalf("unrelated arguments were dropped: %v", args)
	}

	// Without the flag the generated key still wins over a manual one.
	cfg.Delay.PreserveRoutingKey = false
	args = delaySourceArgs(cfg)
	if args["x-dead-letter-routing-key"] != "delayed" {
		t.Fatalf("generated dead-letter key = %v, want delayed", args["x-dead-letter-routing-key"])
	}

	plain := delayQueueConfig(5 * time.Second)
	plain.Delay = nil
	if _, ok := delaySourceArgs(plain)["x-message-ttl"]; ok {
		t.Fatal("a queue without a delay workflow must not declare a TTL")
	}
}
