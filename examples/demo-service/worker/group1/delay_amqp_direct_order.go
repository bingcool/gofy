package group1

import (
	"context"
	"time"

	"go.uber.org/zap"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofyworker "github.com/bingcool/gofy/support/worker"
)

// orderAMQPDelay is the RabbitMQ delayed subscription. Unlike the RocketMQ one,
// v5 has no topic type to switch on: the delay lives in the topology. A message
// is routed into demo.order.delay.5s, waits for its TTL, and the broker
// dead-letters it into demo.order.delay.ready, which is the queue this
// consumer actually reads.
//
// Both exchanges are separate from demo.orders. Reusing the immediate
// subscription's exchange would deliver every delayed event to
// demo.order.events as well, and the sample could no longer tell the two
// streams apart. Direct exchanges plus one fixed key keep the dead-letter
// routing key unambiguous.
//
// TTL is an upper bound, not the delay itself: the producer sets the actual
// per-message Delay. The headroom lets that number change without redeploying
// the queue (x-message-ttl cannot be changed on an existing queue). Keep one
// delay per staging queue — RabbitMQ only checks the head of the queue for
// expiry, so mixing waits would let a long one block a short one behind it.
//
// orderAMQPDelay 是 RabbitMQ 延迟订阅。与 RocketMQ 不同，这里没有"DELAY 类型 topic"
// 可切换：延迟体现在拓扑上。消息先路由进 demo.order.delay.5s，等 TTL 到期后由 broker
// 死信转发到 demo.order.delay.ready —— 后者才是本消费者真正读取的队列。
//
// 两个 exchange 都与 demo.orders 分开。若复用立即订阅的 exchange，每条延迟事件也会
// 投进 demo.order.events，样例就分不清两条流了。用 direct exchange 加一个固定 key，
// 死信 routing key 就没有歧义。
//
// TTL 是上限而不是延迟本身：实际延迟由生产端的每消息 Delay 决定。留出余量后，改这个
// 数字不必重建队列（已存在队列的 x-message-ttl 不能修改）。一个暂存队列只放一种延迟
// ——RabbitMQ 只在队头判定过期，混用等待时长会让长延迟挡住后面的短延迟。
var orderAMQPDelay = gofyconsumer.DelayAMQPConfig{
	Name:              "order-amqp-delay-consumer",
	Instance:          "default",
	TTL:               10 * time.Second,
	DelayExchange:     "demo.orders.delay",
	DelayExchangeType: "direct",
	DelayQueue:        "demo.order.delay.5s",
	DelayBindingKey:   "order.delayed",
	DelayRoutingKey:   "order.delayed",
	Exchange:          "demo.orders.delay.ready",
	ExchangeType:      "direct",
	Queue:             "demo.order.delay.ready",
	BindingKey:        "order.delayed",
	RoutingKey:        "order.delayed",
	Concurrency:       1,
	Idempotency:       true,
	Handler:           handleOrderAMQPDelay,
}

// handleOrderAMQPDelay reports how long the broker actually held the message,
// then runs the same business branch as every other consumer.
//
// Delivery.Timestamp is the producer's event time and survives dead-lettering;
// without it the sample would have to parse the broker's x-death header to
// answer "did the delay happen?".
// handleOrderAMQPDelay 先记下 broker 实际保管了多久，再走与其它消费者共用的业务分支。
//
// Delivery.Timestamp 是生产者写入的事件时间，死信转发后依然保留；否则样例就得解析
// broker 的 x-death 头才能回答"延迟到底生效了没有"。
func handleOrderAMQPDelay(ctx context.Context, cfg gofyconsumer.DelayAMQPConfig, msg gofyconsumer.DelayAMQPMessage) error {
	if rt, ok := gofyworker.RuntimeFrom(ctx); ok && rt.Logger != nil {
		var waited time.Duration
		if !msg.Delivery.Timestamp.IsZero() {
			waited = time.Since(msg.Delivery.Timestamp).Truncate(time.Millisecond)
		}
		rt.Logger.Info("delayed order event arrived",
			zap.String("consumer", cfg.Name),
			zap.String("source", "amqp_delay"),
			zap.String("event_id", msg.Envelope.ID),
			zap.String("delay_queue", cfg.DelayQueue),
			zap.Duration("waited", waited),
			zap.Int32("attempt", msg.Attempt),
		)
	}
	return applyOrderEvent(ctx, msg.Envelope)
}
