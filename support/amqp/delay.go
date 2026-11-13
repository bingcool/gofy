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
	// TTL is the queue-level message TTL, at least 1ms.
	// TTL 是队列级消息 TTL，至少 1ms。
	TTL time.Duration
	// Target is where expired messages are routed.
	// Target 是过期消息被路由到的目的地。
	Target Topology
}

func (d DelayConfig) validate() error {
	if d.TTL < time.Millisecond {
		return errors.New("amqp: delay ttl must be at least 1ms")
	}
	return d.Target.validate(true)
}

func (d DelayConfig) declareTarget(ch *amqp091.Channel) error {
	return d.Target.declare(ch)
}

// declareDelaySource declares the staging exchange and a queue whose
// arguments forward expired messages to the target.
// declareDelaySource 声明暂存 exchange，以及一个通过参数把过期消息转发到
// Target 的队列。
func (c QueueConfig) declareDelaySource(ch *amqp091.Channel) error {
	delay := c.Delay
	args := mergeArgs(c.Topology.Arguments, amqp091.Table{
		"x-dead-letter-exchange":    delay.Target.Exchange,
		"x-dead-letter-routing-key": delay.Target.routingKey(),
		// RabbitMQ expects milliseconds for queue-level TTL.
		// RabbitMQ 的队列级 TTL 以毫秒为单位。
		"x-message-ttl": delay.TTL.Milliseconds(),
	})
	if err := c.Topology.declareExchange(ch); err != nil {
		return err
	}
	if err := c.Topology.declareQueueWith(ch, args); err != nil {
		return fmt.Errorf("amqp: declare delay queue: %w", err)
	}
	return nil
}
