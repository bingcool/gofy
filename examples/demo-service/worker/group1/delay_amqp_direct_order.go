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
// is routed into demo.order.delay.staging, waits for the expiration set at
// publish time, and the broker dead-letters it into demo.order.delay.ready,
// which is the queue this consumer actually reads.
// Both exchanges are separate from demo.orders. Reusing the immediate
// subscription's exchange would deliver every delayed event to
// demo.order.events as well, and the sample could no longer tell the two
// streams apart. Direct exchanges plus one fixed key keep the dead-letter
// routing key unambiguous.
// There is deliberately no TTL here. Leaving it at zero means no x-message-ttl
// is declared, so the producer alone decides the wait through
// amqp.PublishOptions.Delay — the same shape as the RocketMQ and Redis sinks.
// A queue TTL would be a second way to set the delay, and it would silently
// clamp any larger per-message Delay because the broker applies the smaller of
// the two. It also hid mistakes: forgetting Delay made a message quietly arrive
// late instead of failing. Without one, Publish requires Delay, so an undated
// message is rejected at the call site rather than sitting in the staging queue
// forever.
// The staging queue is named "staging" rather than "5s" because the delay is no
// longer a property of the queue. The new name also avoids
// PRECONDITION_FAILED — a queue declared with x-message-ttl cannot be
// redeclared without it. Keep one delay per staging queue: RabbitMQ only checks
// the head of a queue for expiry, so mixing waits lets a long one block a short
// one behind it.
// orderAMQPDelay 是 RabbitMQ 延迟订阅。与 RocketMQ 不同，这里没有"DELAY 类型 topic"
// 可切换：延迟体现在拓扑上。消息先路由进 demo.order.delay.staging，等发布时设定的
// 过期时间到了，由 broker 死信转发到 demo.order.delay.ready —— 后者才是本消费者
// 真正读取的队列。
// 两个 exchange 都与 demo.orders 分开。若复用立即订阅的 exchange，每条延迟事件也会
// 投进 demo.order.events，样例就分不清两条流了。用 direct exchange 加一个固定 key，
// 死信 routing key 就没有歧义。
// 这里刻意不写 TTL。留零即不声明 x-message-ttl，等多久完全由生产端通过
// amqp.PublishOptions.Delay 决定，与 RocketMQ、Redis 两个 sink 写法一致。队列 TTL
// 是第二套延迟写法，且 broker 取两者较小值，会静默截断更大的每消息 Delay；它还会
// 掩盖错误：漏写 Delay 时消息只是悄悄迟到，而不是报错。没有队列 TTL 后，Publish
// 强制要求 Delay，未定延迟的消息在调用点就被拒绝，不会永久卡在暂存队列里。
// 暂存队列叫 staging 而不叫 5s，因为延迟不再是队列的属性。改名也避开
// PRECONDITION_FAILED：已带 x-message-ttl 声明的队列，不能再以不带该参数的方式
// 重新声明。一个暂存队列只放一种延迟——RabbitMQ 只在队头判定过期，混用等待时长
// 会让长延迟挡住后面的短延迟。
var orderAMQPDelay = gofyconsumer.DelayAMQPConfig{
	Name:              "order-amqp-delay-consumer",
	Instance:          "default",
	DelayExchange:     "demo.orders.delay",
	DelayExchangeType: "direct",
	DelayQueue:        "demo.order.delay.staging",
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
