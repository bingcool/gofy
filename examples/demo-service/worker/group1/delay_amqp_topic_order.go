package group1

import (
	"context"
	"time"

	"go.uber.org/zap"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofyworker "github.com/bingcool/gofy/support/worker"
)

// orderAMQPDelayTopic is the topic flavour of the same TTL + dead-letter
// workflow. Both halves are topic exchanges bound with order.*, so the routing
// key decides which queue a message goes to. The staging topology leaves
// RoutingKey empty on purpose: a binding pattern is not a routing key, and the
// producer supplies the concrete key (order.paid) per message instead.
// PreserveRoutingKey is what makes this work. Without it the broker would
// replace every message's key with the target's fixed key on dead-lettering, so
// a topic binding could never distinguish order.paid from order.cancelled —
// and a literal order.* would not even match a concrete binding. With it the
// broker forwards the original key, which the handler logs below.
// Like the direct variant, this subscription declares no queue TTL: the delay
// comes from amqp.PublishOptions.Delay on every publish. The staging queue is
// named "staging" so a queue previously declared with x-message-ttl cannot
// collide with it on redeclaration.
// orderAMQPDelayTopic 是同一套 TTL + 死信流程的 topic 版本。两半都是 topic exchange，
// 绑定 order.*，由 routing key 决定消息进哪个队列。暂存拓扑刻意不写 RoutingKey：
// 绑定 pattern 不是 routing key，具体键（order.paid）由生产端逐条给出。
// PreserveRoutingKey 是这套配置能成立的关键。不开时 broker 会在死信时把每条消息的
// 键都换成目标拓扑的固定值，topic 绑定就再也分不出 order.paid 与 order.cancelled，
// 字面的 order.* 甚至匹配不上具体的绑定。开启后 broker 转发原始键，handler 会把它
// 打到日志里。与 direct 版本一样不声明队列 TTL，延迟由每次发布的
// amqp.PublishOptions.Delay 决定；暂存队列改叫 staging，避免与曾带 x-message-ttl
// 声明的旧队列重名冲突。
var orderAMQPDelayTopic = gofyconsumer.DelayAMQPConfig{
	Name:               "order-amqp-delay-topic-consumer",
	Instance:           "default",
	PreserveRoutingKey: true,
	DelayExchange:      "demo.orders.delay.topic",
	DelayExchangeType:  "topic",
	DelayQueue:         "demo.order.delay.topic.staging",
	DelayBindingKey:    "order.*",
	Exchange:           "demo.orders.delay.topic.ready",
	ExchangeType:       "topic",
	Queue:              "demo.order.delay.topic.ready",
	BindingKey:         "order.*",
	Concurrency:        1,
	Idempotency:        true,
	Handler:            handleOrderAMQPDelayTopic,
}

// handleOrderAMQPDelayTopic logs the routing key the broker forwarded. It is
// the observable proof that the dead-letter hop kept order.paid rather than
// substituting a fixed key.
// handleOrderAMQPDelayTopic 记录 broker 转发过来的 routing key。它是"死信这一跳保留了
// order.paid、没有被换成固定键"的可观测证据。
func handleOrderAMQPDelayTopic(ctx context.Context, cfg gofyconsumer.DelayAMQPConfig, msg gofyconsumer.DelayAMQPMessage) error {
	if rt, ok := gofyworker.RuntimeFrom(ctx); ok && rt.Logger != nil {
		var waited time.Duration
		if !msg.Delivery.Timestamp.IsZero() {
			waited = time.Since(msg.Delivery.Timestamp).Truncate(time.Millisecond)
		}
		rt.Logger.Info("delayed order event arrived",
			zap.String("consumer", cfg.Name),
			zap.String("source", "amqp_delay_topic"),
			zap.String("event_id", msg.Envelope.ID),
			zap.String("delay_queue", cfg.DelayQueue),
			zap.String("routing_key", msg.Delivery.RoutingKey),
			zap.Duration("waited", waited),
			zap.Int32("attempt", msg.Attempt),
		)
	}
	return applyOrderEvent(ctx, msg.Envelope)
}
