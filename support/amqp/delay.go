package amqp

import (
	"errors"
	"fmt"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

// DelayConfig turns a Queue into a TTL + dead-letter delay workflow.
// Publishing declares both sides: the Queue's own Topology is the staging
// exchange/queue that holds messages until they expire, and Target is the
// final exchange/queue that receives them. Consuming reads from Target.Queue,
// because the staging queue has no consumer at all.
// DelayConfig 把 Queue 变成 TTL + 死信的延迟工作流。发布侧会声明两半拓扑：Queue
// 自身的 Topology 是保管消息直到过期的暂存 exchange/queue，Target 是最终接收消息的
// exchange/queue。消费读的是 Target.Queue，暂存队列上完全没有消费者。
//
// Two modes are available. The recommended one leaves TTL at zero: no
// x-message-ttl is declared and PublishOptions.Delay is mandatory, so the
// producer alone decides how long each message waits. A positive TTL keeps the
// classic fixed-delay queue: messages published without a Delay inherit the
// queue TTL, which is also an upper bound because the broker applies the
// smaller of the two values.
// 有两种模式。推荐把 TTL 留零：队列不声明 x-message-ttl，PublishOptions.Delay
// 变成必填，每条消息等多久完全由生产端决定。TTL 为正时保留经典的固定延迟队列：
// 未带 Delay 的消息继承队列 TTL，且它同时是上限——broker 取两者中较小值。
//
// The zero-TTL mode is stricter on purpose. With a queue TTL, forgetting Delay
// does not fail: the message silently waits for the queue TTL and arrives late,
// which hides the bug. Without one, an undated message would never expire and
// sit in the staging queue forever, so Publish rejects it at the call site.
// 零 TTL 模式刻意更严格。有队列 TTL 时漏写 Delay 不会报错，消息只是悄悄等满队列 TTL
// 后迟到，把 bug 藏了起来；没有队列 TTL 时未定延迟的消息永不过期、会一直卡在暂存
// 队列里，因此 Publish 在调用点直接拒绝。
type DelayConfig struct {
	// TTL is the optional queue-level message TTL. Zero (the default) means
	// publish-time only: no x-message-ttl is declared and every message must
	// carry PublishOptions.Delay. A positive value must be at least 1ms and
	// becomes both the default delay and the upper bound for per-message ones.
	// TTL 是可选的队列级消息 TTL。零值（默认）表示只在发布时定延迟：不声明
	// x-message-ttl，每条消息都必须带 PublishOptions.Delay。正值至少 1ms，既是默认
	// 延迟，也是单条消息延迟的上限。
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
	if d.TTL < 0 || (d.TTL > 0 && d.TTL < time.Millisecond) {
		return errors.New("amqp: delay ttl must be 0 or at least 1ms")
	}
	return d.Target.validate(true)
}

// delaySourceArgs builds the staging queue arguments. Keeping it a pure
// function lets both TTL modes and the topic path — where a fixed dead-letter
// routing key would be wrong — be tested without a broker.
// delaySourceArgs 生成暂存队列参数。抽成纯函数后，两种 TTL 模式与 topic 路径（固定
// 死信 routing key 会出错）都无需 broker 就能测试。
func delaySourceArgs(cfg QueueConfig) amqp091.Table {
	delay := cfg.Delay
	if delay == nil {
		return mergeArgs(cfg.Topology.Arguments, nil)
	}
	extra := amqp091.Table{
		"x-dead-letter-exchange":    delay.Target.Exchange,
		"x-dead-letter-routing-key": delay.Target.routingKey(),
	}
	if delay.TTL > 0 {
		// RabbitMQ expects milliseconds for queue-level TTL.
		// RabbitMQ 的队列级 TTL 以毫秒为单位。
		extra["x-message-ttl"] = delay.TTL.Milliseconds()
	}
	args := mergeArgs(cfg.Topology.Arguments, extra)
	if delay.TTL == 0 {
		// A hand-written value would silently reintroduce the queue-level TTL
		// that the zero-TTL mode is defined by not having.
		// 手工写入的值会悄悄把零 TTL 模式刻意不声明的队列级 TTL 带回来。
		delete(args, "x-message-ttl")
	}
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
// RabbitMQ only checks the message at the head of a queue for expiry, so a
// staging queue that mixes different delays lets a long wait hold up shorter
// ones behind it. Keeping one delay per staging queue (or one queue per delay
// band) avoids that head-of-line blocking. A delay on a queue with no
// dead-letter target is rejected instead of dropping the message at expiry.
// messageExpiration 计算实际生效的单条消息 TTL。
// RabbitMQ 只在队头判定消息是否过期，因此同一暂存队列里混用不同延迟时，前面的长
// 延迟会挡住后面的短延迟。一个暂存队列只放一种延迟（或按延迟分档建队列）可避免这种
// 队头阻塞。没有死信目标的队列上设置延迟会被拒绝，而不是让消息到期后凭空消失。
func messageExpiration(cfg QueueConfig, opts PublishOptions) (time.Duration, error) {
	if opts.Delay == 0 {
		if opts.Expiration > 0 {
			return opts.Expiration, nil
		}
		// Without a queue TTL there is nothing that could ever expire this
		// message. Requiring a per-message delay here is what makes the
		// publish-time-only mode safe: the mistake fails at the call site
		// instead of leaving a message stranded forever.
		// 没有队列 TTL 时，这条消息再也不会过期。这里要求单条消息必须带延迟，正是
		// 纯发布端模式安全的原因：错误在调用点暴露，而不是让消息永久滞留。
		if cfg.Delay != nil && cfg.Delay.TTL == 0 {
			return 0, ErrDelayRequired
		}
		return 0, nil
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
	// Only a declared queue TTL can clamp a per-message delay. In the
	// publish-time-only mode any duration the broker accepts is valid.
	// 只有声明了队列 TTL 才可能截断单条消息的延迟。纯发布端模式下，broker 接受的
	// 任意时长都合法。
	if cfg.Delay.TTL > 0 && opts.Delay > cfg.Delay.TTL {
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
