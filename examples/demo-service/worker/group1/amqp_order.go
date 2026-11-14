package group1

import (
	"context"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
)

// orderAMQP is the RabbitMQ subscription for the same events.
//
// The queue declares its own topology, so registering this consumer is what
// creates the exchange, queue and binding — one file is enough to go from
// nothing to a working subscription.
// orderAMQP 是同一批事件的 RabbitMQ 订阅。
//
// 队列自己声明 topology，因此注册这个消费者就会创建 exchange、队列与绑定——一个文件
// 就能从零到可用的订阅。
var orderAMQP = gofyconsumer.AMQPConfig{
	Name: "order-amqp-consumer",
	// Instance picks the amqp.default block: hosts, credentials and vhost live
	// there, so a second cluster is a new block, not a new field here.
	// Instance 指向 config.yaml 的 amqp.default：hosts、认证与 vhost 在那边，
	// 换一个集群就是加一个新块，而不是在这里改字段。
	Instance:     "default",
	Exchange:     "demo.orders",
	ExchangeType: "topic",
	Queue:        "demo.order.events",
	BindingKey:   "order.*",
	RoutingKey:   "order.*",
	Idempotency:  true,
	Handler:      handleOrderAMQP,
}

// handleOrderAMQP is the business branch for one delivery.
//
// cfg names the exchange, queue and binding key this delivery came through, so
// a handler that serves several queues can tell them apart by parameter instead
// of by closure. msg.Delivery is the native amqp091.Delivery, which is what a
// manual Ack takes when AutoAck is off.
// handleOrderAMQP 是一次投递的业务分支。
//
// cfg 给出这次投递经过的 exchange、queue 与 binding key，因此服务多个队列的 handler
// 可以按参数区分，而不是靠闭包。msg.Delivery 是原生 amqp091.Delivery，关闭 AutoAck
// 后手动 Ack 用的就是它。
func handleOrderAMQP(ctx context.Context, cfg gofyconsumer.AMQPConfig, msg gofyconsumer.AMQPMessage) error {
	return applyOrderEvent(ctx, msg.Envelope)
}
