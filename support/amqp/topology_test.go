package amqp

import (
	"errors"
	"testing"
	"time"
)

func TestTopologyValidate(t *testing.T) {
	cases := []struct {
		name         string
		topo         Topology
		requireQueue bool
		ok           bool
	}{
		{
			"direct defaults keys",
			Topology{Exchange: "orders", Queue: "orders", RoutingKey: "created"},
			true,
			true,
		},
		{
			"direct matching keys",
			Topology{Exchange: "orders", ExchangeType: ExchangeDirect, Queue: "orders", RoutingKey: "created", BindingKey: "created"},
			true,
			true,
		},
		{
			"direct mismatched keys",
			Topology{Exchange: "orders", Queue: "orders", RoutingKey: "created", BindingKey: "paid"},
			true,
			false,
		},
		{
			"direct queue without keys",
			Topology{Exchange: "orders", Queue: "orders"},
			true,
			false,
		},
		{
			"fanout without keys",
			Topology{Exchange: "events", ExchangeType: ExchangeFanout, Queue: "events"},
			true,
			true,
		},
		{
			"topic with keys",
			Topology{Exchange: "events", ExchangeType: ExchangeTopic, Queue: "events", BindingKey: "order.*", RoutingKey: "order.created"},
			true,
			true,
		},
		{
			"publish only exchange",
			Topology{Exchange: "events", ExchangeType: ExchangeTopic},
			false,
			true,
		},
		{
			"consume without queue",
			Topology{Exchange: "events", ExchangeType: ExchangeTopic},
			true,
			false,
		},
		{
			"missing exchange",
			Topology{Queue: "orders", RoutingKey: "created"},
			true,
			false,
		},
		{
			"invalid type",
			Topology{Exchange: "orders", ExchangeType: "stream", Queue: "orders"},
			true,
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.topo.validate(tc.requireQueue)
			if tc.ok && err != nil {
				t.Fatalf("validate: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("validate accepted an invalid topology")
			}
		})
	}
}

func TestQueueConfigValidateDelay(t *testing.T) {
	target := Topology{Exchange: "orders.result", Queue: "orders.result", RoutingKey: "done"}
	base := QueueConfig{Topology: Topology{Exchange: "orders.delay", Queue: "orders.delay", RoutingKey: "done"}}

	valid := base
	valid.Delay = &DelayConfig{TTL: time.Second, Target: target}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid delay config: %v", err)
	}

	tooFast := base
	tooFast.Delay = &DelayConfig{TTL: 500 * time.Microsecond, Target: target}
	if err := tooFast.validate(); err == nil {
		t.Fatal("accepted a sub-millisecond ttl")
	}

	noTargetQueue := base
	noTargetQueue.Delay = &DelayConfig{TTL: time.Second, Target: Topology{Exchange: "orders.result"}}
	if err := noTargetQueue.validate(); !errors.Is(err, ErrQueueRequired) {
		t.Fatalf("target queue error = %v", err)
	}

	noSourceQueue := QueueConfig{Topology: Topology{Exchange: "orders.delay"}}
	noSourceQueue.Delay = &DelayConfig{TTL: time.Second, Target: target}
	if err := noSourceQueue.validate(); !errors.Is(err, ErrQueueRequired) {
		t.Fatalf("source queue error = %v", err)
	}
}

func TestQueueConfigDirectKeyErrors(t *testing.T) {
	mismatch := QueueConfig{Topology: Topology{Exchange: "orders", Queue: "orders", RoutingKey: "a", BindingKey: "b"}}
	if err := mismatch.validate(); !errors.Is(err, ErrRoutingKeyMismatch) {
		t.Fatalf("mismatch error = %v", err)
	}

	missing := QueueConfig{Topology: Topology{Exchange: "orders", Queue: "orders"}}
	if err := missing.validate(); !errors.Is(err, ErrDirectKeyRequired) {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestMergeArgsOverlays(t *testing.T) {
	base := map[string]any{"x-max-priority": 10, "x-message-ttl": 1}
	merged := mergeArgs(base, map[string]any{"x-message-ttl": 2})

	if merged["x-max-priority"] != 10 || merged["x-message-ttl"] != 2 {
		t.Fatalf("merged = %v", merged)
	}
	if base["x-message-ttl"] != 1 {
		t.Fatal("mergeArgs mutated the base table")
	}
	if mergeArgs(nil, nil) != nil {
		t.Fatal("empty merge should return nil")
	}
}
