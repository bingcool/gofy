package amqp

import (
	"errors"
	"fmt"
	"strings"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

// Exchange types supported by RabbitMQ.
const (
	ExchangeDirect  = "direct"
	ExchangeFanout  = "fanout"
	ExchangeTopic   = "topic"
	ExchangeHeaders = "headers"
)

var (
	ErrExchangeRequired    = errors.New("amqp: exchange is required")
	ErrQueueRequired       = errors.New("amqp: queue is required")
	ErrInvalidExchangeType = errors.New("amqp: invalid exchange type")
	ErrDirectKeyRequired   = errors.New("amqp: direct exchange requires a routing key and a binding key")
	ErrRoutingKeyMismatch  = errors.New("amqp: direct exchange requires routing key == binding key")
)

// Topology declares one exchange, queue and binding.
//
// For a direct exchange the routing key and the binding key must match;
// either field may be left empty and is then copied from the other. Fanout
// ignores both keys, and topic/headers use them as usual. Leave Queue empty
// for a publish-only fanout/topic exchange.
type Topology struct {
	Exchange     string
	ExchangeType string // direct (default), fanout, topic or headers
	Queue        string
	BindingKey   string
	RoutingKey   string
	Durable      bool
	AutoDelete   bool
	Exclusive    bool
	Internal     bool
	// Passive checks the broker-side declaration instead of creating it.
	Passive bool
	// NoWait skips waiting for the broker's declare-ok reply.
	NoWait    bool
	Arguments amqp091.Table
}

// QueueConfig describes how a Queue declares and publishes to its topology.
// Set Delay to route the workflow through a TTL queue instead.
type QueueConfig struct {
	Topology
	// Confirm switches publishing to RabbitMQ publisher confirms. Publish then
	// waits for the broker's ack and reports nacks as errors.
	Confirm bool
	// Delay turns this Queue into a delayed-message workflow.
	Delay *DelayConfig
}

func (c QueueConfig) validate() error {
	if err := c.Topology.validate(false); err != nil {
		return err
	}
	if c.Delay != nil {
		// 延迟工作流必须有一个暂存队列等待 TTL，否则消息发布后无处存放。
		if strings.TrimSpace(c.Queue) == "" {
			return ErrQueueRequired
		}
		return c.Delay.validate()
	}
	return nil
}

func (t Topology) validate(requireQueue bool) error {
	if strings.TrimSpace(t.Exchange) == "" {
		return ErrExchangeRequired
	}
	switch t.exchangeType() {
	case ExchangeDirect, ExchangeFanout, ExchangeTopic, ExchangeHeaders:
	default:
		return fmt.Errorf("%w %q", ErrInvalidExchangeType, t.ExchangeType)
	}
	if requireQueue && strings.TrimSpace(t.Queue) == "" {
		return ErrQueueRequired
	}
	if strings.TrimSpace(t.Queue) != "" && t.exchangeType() == ExchangeDirect {
		routingKey, bindingKey := t.routingKey(), t.bindingKey()
		if routingKey == "" || bindingKey == "" {
			return ErrDirectKeyRequired
		}
		if routingKey != bindingKey {
			return ErrRoutingKeyMismatch
		}
	}
	return nil
}

func (t Topology) exchangeType() string {
	if strings.TrimSpace(t.ExchangeType) == "" {
		return ExchangeDirect
	}
	return strings.ToLower(strings.TrimSpace(t.ExchangeType))
}

func (t Topology) routingKey() string {
	if strings.TrimSpace(t.RoutingKey) != "" {
		return t.RoutingKey
	}
	return t.BindingKey
}

func (t Topology) bindingKey() string {
	if strings.TrimSpace(t.BindingKey) != "" {
		return t.BindingKey
	}
	return t.RoutingKey
}

// declare creates the exchange and, when Queue is set, the queue and binding.
func (t Topology) declare(ch *amqp091.Channel) error {
	if err := t.declareExchange(ch); err != nil {
		return err
	}
	if strings.TrimSpace(t.Queue) == "" {
		return nil
	}
	return t.declareQueue(ch)
}

func (t Topology) declareExchange(ch *amqp091.Channel) error {
	kind := t.exchangeType()
	var err error
	if t.Passive {
		err = ch.ExchangeDeclarePassive(t.Exchange, kind, t.Durable, t.AutoDelete, t.Internal, t.NoWait, t.Arguments)
	} else {
		err = ch.ExchangeDeclare(t.Exchange, kind, t.Durable, t.AutoDelete, t.Internal, t.NoWait, t.Arguments)
	}
	if err != nil {
		return fmt.Errorf("amqp: declare exchange %q: %w", t.Exchange, err)
	}
	return nil
}

func (t Topology) declareQueue(ch *amqp091.Channel) error {
	return t.declareQueueWith(ch, t.Arguments)
}

func (t Topology) declareQueueWith(ch *amqp091.Channel, args amqp091.Table) error {
	var err error
	if t.Passive {
		_, err = ch.QueueDeclarePassive(t.Queue, t.Durable, t.AutoDelete, t.Exclusive, t.NoWait, args)
	} else {
		_, err = ch.QueueDeclare(t.Queue, t.Durable, t.AutoDelete, t.Exclusive, t.NoWait, args)
	}
	if err != nil {
		return fmt.Errorf("amqp: declare queue %q: %w", t.Queue, err)
	}
	if err := ch.QueueBind(t.Queue, t.bindingKey(), t.Exchange, t.NoWait, nil); err != nil {
		return fmt.Errorf("amqp: bind queue %q to exchange %q: %w", t.Queue, t.Exchange, err)
	}
	return nil
}

// mergeArgs copies base and overlays extra, so callers keep their own
// arguments while generated delay arguments win.
func mergeArgs(base, extra amqp091.Table) amqp091.Table {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := make(amqp091.Table, len(base)+len(extra))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}
