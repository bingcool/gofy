package amqp

import (
	"errors"
	"fmt"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

// DelayConfig turns a Queue into a TTL + dead-letter delay workflow.
//
// Publishing declares both sides: the Queue's own Topology is the delay
// exchange/queue that holds messages for TTL, and Target is the final
// exchange/queue that receives them when they expire. Consuming reads from
// Target.Queue, because the delay queue itself is only a staging area.
//
// 发布侧会先声明 Target，再声明带 x-message-ttl / x-dead-letter-* 参数的延迟
// 队列，避免过期消息因为目标交换机不存在而丢失。
type DelayConfig struct {
	// TTL is the queue-level message TTL, at least 1ms. It is both the delay
	// for messages published without PublishOptions.Delay and the upper bound
	// for those published with one, since the broker applies the smaller of
	// the two values.
	// TTL 是队列级消息 TTL，至少 1ms。它既是未设置 PublishOptions.Delay 的消息
	// 的延迟，也是设置了该字段的消息的延迟上限——broker 取两者中较小值。
	TTL time.Duration
	// Target is where expired messages are routed.
	// Target 是过期消息被路由到的目的地。
	Target Topology
	// PreserveRoutingKey leaves x-dead-letter-routing-key unset, so the broker
	// dead-letters with the message's original routing key. A topic target
	// needs this: a fixed key overwrites order.paid with one literal value,
	// collapsing topic routing and possibly missing the target binding.
	// PreserveRoutingKey 让队列不设置 x-dead-letter-routing-key，broker 死信时沿用
	// 消息原始的 routing key。目标是 topic exchange 时必须开启：固定 key 会把
	// order.paid 覆盖成同一个字面值，topic 分发失效，甚至匹配不上绑定而丢消息。
	PreserveRoutingKey bool
}

func (d DelayConfig) validate() error {
	if d.TTL < time.Millisecond {
		return errors.New("amqp: delay ttl must be at least 1ms")
	}
	return d.Target.validate(true)
}

// delaySourceArgs builds the staging queue arguments. Keeping it a pure
// function lets the topic path — where a fixed dead-letter routing key would
// be wrong — be tested without a broker.
// delaySourceArgs 生成暂存队列参数。抽成纯函数后，topic 这条路径（固定死信 routing key
// 会出错）无需 broker 就能测试。
func delaySourceArgs(cfg QueueConfig) amqp091.Table {
	delay := cfg.Delay
	if delay == nil {
		return mergeArgs(cfg.Topology.Arguments, nil)
	}
	args := mergeArgs(cfg.Topology.Arguments, amqp091.Table{
		"x-dead-letter-exchange":    delay.Target.Exchange,
		"x-dead-letter-routing-key": delay.Target.routingKey(),
		// RabbitMQ expects milliseconds for queue-level TTL.
		// RabbitMQ 的队列级 TTL 以毫秒为单位。
		"x-message-ttl": delay.TTL.Milliseconds(),
	})
	if delay.PreserveRoutingKey {
		// Removing even a caller-supplied value is deliberate: setting the key
		// at all would override the original routing key on the way out.
		// 连调用方手工设置的值也一并删掉是刻意的：只要该参数存在，就会在死信时覆盖
		// 消息原始的 routing key。
		delete(args, "x-dead-letter-routing-key")
	}
	return args
}

// messageExpiration resolves the effective per-message TTL.
//
// RabbitMQ only checks the message at the head of a queue for expiry, so a
// staging queue that mixes different delays lets a long wait hold up shorter
// ones behind it. Keeping one delay per staging queue (or one queue per delay
// band) avoids that head-of-line blocking. Delays past the queue TTL are
// rejected instead of being silently clamped, and a delay on a queue with no
// dead-letter target is rejected instead of dropping the message at expiry.
// messageExpiration 计算实际生效的单条消息 TTL。
//
// RabbitMQ 只在队头判定消息是否过期，因此同一暂存队列里混用不同延迟时，前面的长
// 延迟会挡住后面的短延迟。一个暂存队列只放一种延迟（或按延迟分档建多个队列）可避免
// 这种队头阻塞。超过队列 TTL 的延迟会被拒绝而不是静默截断；没有死信目标的队列上设置
// 延迟也会被拒绝，而不是让消息到期后凭空消失。
func messageExpiration(cfg QueueConfig, opts PublishOptions) (time.Duration, error) {
	if opts.Delay == 0 {
		return opts.Expiration, nil
	}
	if opts.Expiration > 0 {
		return 0, errors.New("amqp: delay and expiration are mutually exclusive")
	}
	if opts.Delay < time.Millisecond {
		return 0, fmt.Errorf("%w: %s must be at least 1ms", ErrInvalidDelay, opts.Delay)
	}
	if cfg.Delay == nil {
		return 0, ErrDelayRequiresWorkflow
	}
	if opts.Delay > cfg.Delay.TTL {
		return 0, fmt.Errorf("%w: %s exceeds the staging queue ttl %s",
			ErrInvalidDelay, opts.Delay, cfg.Delay.TTL)
	}
	return opts.Delay, nil
}

func (d DelayConfig) declareTarget(ch *amqp091.Channel) error {
	return d.Target.declare(ch)
}

// declareDelaySource declares the staging exchange and a queue whose
// arguments forward expired messages to the target.
// declareDelaySource 声明暂存 exchange，以及一个通过参数把过期消息转发到
// Target 的队列。
func (c QueueConfig) declareDelaySource(ch *amqp091.Channel) error {
	args := delaySourceArgs(c)
	if err := c.Topology.declareExchange(ch); err != nil {
		return err
	}
	if err := c.Topology.declareQueueWith(ch, args); err != nil {
		return fmt.Errorf("amqp: declare delay queue: %w", err)
	}
	return nil
}
