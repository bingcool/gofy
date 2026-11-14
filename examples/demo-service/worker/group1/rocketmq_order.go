package group1

import (
	"context"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
)

// orderRocketMQ is the RocketMQ subscription for the same events.
//
// RocketMQ consumers are single-use, so the worker runs one instance and the
// SDK's own pull workers provide the parallelism.
// orderRocketMQ 是同一批事件的 RocketMQ 订阅。
//
// RocketMQ 消费者是一次性对象，因此 worker 只跑一个实例，并行度由 SDK 自己的拉取
// worker 提供。
var orderRocketMQ = gofyconsumer.RocketMQConfig{
	Name: "order-rocketmq-consumer",
	// Instance picks the rocketmq.default block: endpoint and ACL live there,
	// while topic, group and tags belong to this subscription.
	// Instance 指向 config.yaml 的 rocketmq.default：endpoint 与 ACL 在那边，
	// topic、group 与 tags 属于这个订阅。
	Instance:    "default",
	Topic:       "demo-order-events",
	Group:       "demo-service-order-consumer",
	Concurrency: 1,
	// Tags is an OR list — the SDK joins the entries with "||" into one
	// subscription expression. Empty, or a single "*", subscribes to every tag.
	// Tags 是"或"关系：SDK 用 "||" 把各元素拼成一个订阅表达式。留空或只有一个
	// "*" 表示订阅全部 tag。
	Tags:        []string{"order"},
	Idempotency: true,
	Handler:     handleOrderRocketMQ,
}

// handleOrderRocketMQ is the business branch for one message.
//
// cfg carries the topic, group and tags of the subscription that received it —
// the tags matter here, since they are what narrowed the subscription in the
// first place. msg.Message is the native RocketMQ message with the broker id,
// keys and delivery attempt.
// handleOrderRocketMQ 是一条消息的业务分支。
//
// cfg 带收到它的订阅的 topic、group 与 tags——这里的 tags 尤其有用，它正是订阅被
// 收窄的原因。msg.Message 是原生 RocketMQ 消息，带 broker 消息 ID、keys 与投递次数。
func handleOrderRocketMQ(ctx context.Context, cfg gofyconsumer.RocketMQConfig, msg gofyconsumer.RocketMQMessage) error {
	return applyOrderEvent(ctx, msg.Envelope)
}
