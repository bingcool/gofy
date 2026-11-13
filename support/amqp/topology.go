package amqp

import (
	"errors"
	"fmt"
	"strings"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

// Exchange types supported by RabbitMQ.
// RabbitMQ 支持的 exchange 类型。
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
// Topology 声明一个 exchange、queue 与 binding。
// direct exchange 要求 routing key 与 binding key 相同；任一为空时会从另一个
// 复制。fanout 忽略这两个 key，topic/headers 按常规语义使用。
// 只做发布的 fanout/topic exchange 可以把 Queue 留空。
type Topology struct {
	Exchange     string
	ExchangeType string // direct (default), fanout, topic or headers；取值 direct（默认）、fanout、topic 或 headers，空值按 direct 处理
	Queue        string
	BindingKey   string
	RoutingKey   string
	Durable      bool
	AutoDelete   bool
	Exclusive    bool
	Internal     bool
	// Passive checks the broker-side declaration instead of creating it.
	// Passive 只检查 broker 端是否已有该声明，不创建它。
	Passive bool
	// NoWait skips waiting for the broker's declare-ok reply.
	// NoWait 跳过等待 broker 的 declare-ok 应答，声明失败只能稍后才发现。
	NoWait    bool
	Arguments amqp091.Table
}

// QueueConfig describes how a Queue declares and publishes to its topology.
// Set Delay to route the workflow through a TTL queue instead.
// QueueConfig 描述 Queue 如何声明 topology 并向其发布。设置 Delay 可改为经由
// TTL 队列的延迟投递流程。
type QueueConfig struct {
	Topology
	// Confirm switches publishing to RabbitMQ publisher confirms. Publish then
	// waits for the broker's ack and reports nacks as errors.
	// Confirm 把发布切换到 RabbitMQ publisher confirms，Publish 会等待 broker
	// 的 ack，并把 nack 作为错误返回。
	Confirm bool
	// Delay turns this Queue into a delayed-message workflow.
	// Delay 把该 Queue 变成延迟消息工作流。
	Delay *DelayConfig
}

func (c QueueConfig) validate() error {
	if err := c.Topology.validate(false); err != nil {
		return err
	}
	if c.Delay != nil {
		// A delay workflow needs a staging queue to hold messages during the TTL,
		// otherwise a published message has nowhere to wait.
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
// declare 创建 exchange；Queue 非空时同时创建 queue 与 binding。
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
// mergeArgs 复制 base 并叠加 extra：调用方保留自己的参数，生成的延迟参数优先。
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
