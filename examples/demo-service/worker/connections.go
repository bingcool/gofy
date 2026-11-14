package worker

import (
	gofyamqp "github.com/bingcool/gofy/support/amqp"
	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofykafka "github.com/bingcool/gofy/support/kafka"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"

	"github.com/yourorg/demo-service/app/config"
	appamqp "github.com/yourorg/demo-service/app/io/amqp"
	appkafka "github.com/yourorg/demo-service/app/io/kafka"
	appredis "github.com/yourorg/demo-service/app/io/redis"
	approcketmq "github.com/yourorg/demo-service/app/io/rocketmq"
)

// ConnectionsFrom turns the application config into the broker table a
// consumer can name.
//
// It lives here rather than in main because it exists only for Register: the
// worker package is the one place that knows a consumer names its broker by
// instance, so the translation from the config blocks belongs next to the call
// that consumes it.
//
// Every decoded instance is kept, including the ones with enabled: false. That
// flag answers "does the HTTP process open this connection at startup", and a
// worker makes the opposite choice by design: it opens a broker only for the
// consumers that are registered, and only inside Run. Filtering here would make
// `enabled: false` silently mean "this worker cannot start", which is not what
// the flag says.
// ConnectionsFrom 把应用配置转成消费者可以按名字引用的 broker 连接表。
//
// 它放在这里而不是 main，是因为它只为 Register 存在：worker 包是唯一知道"消费者按实例名
// 引用 broker"的地方，因此从配置块到连接表的翻译应该紧挨着用它的那次调用。
//
// 所有已解码的实例都保留，包括 enabled: false 的那些。那个标志回答的是"HTTP 进程启动时
// 是否打开这条连接"，而 worker 的取舍正好相反：只为已注册的消费者、且只在 Run 里建连。
// 在这里过滤会让 enabled: false 悄悄变成"这个 worker 起不来"，那不是该标志的含义。
func ConnectionsFrom(cfg *config.Config) gofyconsumer.Connections {
	return gofyconsumer.Connections{
		Kafka:    kafkaConnections(cfg.Kafka),
		AMQP:     amqpConnections(cfg.AMQP),
		RocketMQ: rocketMQConnections(cfg.RocketMQ),
		Redis:    redisConnections(cfg.Redis),
	}
}

// kafkaConnections drops the per-instance enabled flag, which has no meaning
// for a worker: the consumer's own registration is what decides.
// kafkaConnections 去掉每项的 enabled 标志：对 worker 来说它没有意义，决定权在消费者
// 是否被注册。
func kafkaConnections(cfg appkafka.Config) map[string]gofykafka.Config {
	out := make(map[string]gofykafka.Config, len(cfg))
	for name, inst := range cfg {
		out[name] = inst.Config
	}
	return out
}

// amqpConnections is the AMQP counterpart of kafkaConnections.
// amqpConnections 是 kafkaConnections 在 AMQP 上的对应实现。
func amqpConnections(cfg appamqp.Config) map[string]gofyamqp.Config {
	out := make(map[string]gofyamqp.Config, len(cfg))
	for name, inst := range cfg {
		out[name] = inst.Config
	}
	return out
}

// rocketMQConnections is the RocketMQ counterpart of kafkaConnections.
// rocketMQConnections 是 kafkaConnections 在 RocketMQ 上的对应实现。
func rocketMQConnections(cfg approcketmq.Config) map[string]gofyrocketmq.Config {
	out := make(map[string]gofyrocketmq.Config, len(cfg))
	for name, inst := range cfg {
		out[name] = inst.Config
	}
	return out
}

// redisConnections is the Redis counterpart of kafkaConnections.
// redisConnections 是 kafkaConnections 在 Redis 上的对应实现。
func redisConnections(cfg appredis.Config) map[string]gofyredis.Config {
	out := make(map[string]gofyredis.Config, len(cfg))
	for name, inst := range cfg {
		out[name] = inst.Config
	}
	return out
}
