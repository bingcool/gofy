package group1

import (
	"context"
	"time"

	"go.uber.org/zap"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofyworker "github.com/bingcool/gofy/worker"
)

// orderRocketMQDelay subscribes to the DELAY-type topic. RocketMQ v5 rejects a
// delayed message on a NORMAL topic with "current message type not match with
// topic accept message types", so the topic is separate from test-demo:
//
//	mqadmin updateTopic -n <namesrv> -t test-demo-delay \
//	  -c DefaultCluster -a +message.type=DELAY
//
// The tag keeps the delayed stream apart from other producers on that topic,
// and the group is its own so its offsets do not interfere with the immediate
// subscription.
// orderRocketMQDelay 订阅 DELAY 类型的 topic。RocketMQ v5 在 NORMAL topic 上会
// 用 "current message type not match with topic accept message types" 拒绝延迟消息，
// 因此该 topic 与 test-demo 分开，创建命令见上。
//
// tag 用来把延迟流与该 topic 上其它生产者分开；消费组也独立，位点不会与立即订阅互相干扰。
var orderRocketMQDelay = gofyconsumer.RocketMQConfig{
	Name:     "order-rocketmq-delay-consumer",
	Instance: "default",
	Topic:    "test-demo-delay",
	Tags:     []string{"delay"},
	Group:    "demo-service-order-delay-consumer",
	// The broker holds a delayed message until its delivery timestamp, so the
	// long-poll window can stay at the default.
	// broker 会保管延迟消息到投递时刻，长轮询窗口用默认值即可。
	Concurrency: 1,
	Idempotency: true,
	Handler:     handleOrderRocketMQDelay,
}

// handleOrderRocketMQDelay reports both the scheduled delay and the observed
// wait. DeliveryTimestamp is the broker's planned time; BornTimestamp is when
// the producer sent the message.
// handleOrderRocketMQDelay 同时记录计划延迟与实测等待。DeliveryTimestamp 是 broker
// 的计划投递时刻，BornTimestamp 是生产者发出的时刻。
func handleOrderRocketMQDelay(ctx context.Context, cfg gofyconsumer.RocketMQConfig, msg gofyconsumer.RocketMQMessage) error {
	if rt, ok := gofyworker.RuntimeFrom(ctx); ok && rt.Logger != nil {
		var scheduled, waited time.Duration
		if msg.Message != nil {
			if deliverAt := msg.Message.DeliveryTimestamp; !deliverAt.IsZero() && !msg.Message.BornTimestamp.IsZero() {
				scheduled = deliverAt.Sub(msg.Message.BornTimestamp).Truncate(time.Millisecond)
			}
			if !msg.Message.BornTimestamp.IsZero() {
				waited = time.Since(msg.Message.BornTimestamp).Truncate(time.Millisecond)
			}
		}
		rt.Logger.Info("delayed order event arrived",
			zap.String("consumer", cfg.Name),
			zap.String("source", "rocketmq_delay"),
			zap.String("event_id", msg.Envelope.ID),
			zap.Duration("scheduled", scheduled),
			zap.Duration("waited", waited),
			zap.Int32("attempt", msg.Attempt),
		)
	}
	return applyOrderEvent(ctx, msg.Envelope)
}
