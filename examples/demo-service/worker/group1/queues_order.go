package group1

import (
	"context"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
)

// orderQueues is the Redis queue subscription for the same events.
//
// It is the cheapest of the four to run and the weakest guarantee: a popped
// entry is already gone from Redis, so a crash inside the handler loses it.
// handleOrderQueues is where that trade is accepted.
// orderQueues 是同一批事件的 Redis 队列订阅。
//
// 它是四种里运行成本最低、保证也最弱的一种：条目弹出时已从 Redis 删除，handler 崩溃
// 就会丢这一条。这个取舍由 handleOrderQueues 承担。
var orderQueues = gofyconsumer.QueuesConfig{
	Name: "order-queues-consumer",
	// RedisInstance picks the redis.ratelimit block: the queue reuses that pool
	// instead of opening its own. redis.cache and redis.session are different
	// instances, so a misspelled name fails at registration.
	// RedisInstance 指向 config.yaml 的 redis.ratelimit：队列复用它那条连接池，
	// 不再单独开一个。redis.cache 与 redis.session 是别的实例，名字写错会在注册
	// 阶段报错。
	RedisInstance: "ratelimit",
	Key:           "demo:order:events",
	Concurrency:   2,
	Idempotency:   true,
	Handler:       handleOrderQueues,
}

// handleOrderQueues is the business branch for one entry. msg.Message is the
// native queues.Message with its id, retry number and timestamp.
// handleOrderQueues 是一条队列项的业务分支。msg.Message 是原生 queues.Message，
// 带 ID、重试次数与时间戳。
func handleOrderQueues(ctx context.Context, msg gofyconsumer.QueuesMessage) error {
	return applyOrderEvent(ctx, msg.Envelope)
}
