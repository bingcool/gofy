package main

import (
	gofyamqp "github.com/bingcool/gofy/support/amqp"
	gofykafka "github.com/bingcool/gofy/support/kafka"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	"github.com/yourorg/demo-service/app/config"
	appamqp "github.com/yourorg/demo-service/app/io/amqp"
	appkafka "github.com/yourorg/demo-service/app/io/kafka"
	appredis "github.com/yourorg/demo-service/app/io/redis"
	approcketmq "github.com/yourorg/demo-service/app/io/rocketmq"
	appworker "github.com/yourorg/demo-service/worker"
)

// workerDeps turns the application config into what worker.Register needs.
//
// The mapping keeps enabled instances only, so a consumer that names a
// disabled or misspelled instance fails while the groups are being built —
// before the worker process opens a pool, a log file or a broker connection.
// workerDeps 把应用配置转成 worker.Register 需要的形式。
//
// 映射只保留已启用的实例，因此消费者引用未启用或拼错的实例时，会在构造分组阶段
// 就失败——早于 worker 进程打开连接池、日志文件或 broker 连接。
func workerDeps(cfg *config.Config) appworker.Deps {
	return appworker.Deps{
		Group1: cfg.Worker,
		Connections: gofyconsumer.Connections{
			Kafka:    kafkaConnections(cfg.Kafka),
			AMQP:     amqpConnections(cfg.AMQP),
			RocketMQ: rocketMQConnections(cfg.RocketMQ),
			Redis:    redisConnections(cfg.Redis),
		},
	}
}

// kafkaConnections drops the per-instance enabled flag, which has no meaning
// once the map itself is the enabled set.
// kafkaConnections 去掉每项的 enabled 标志：一旦这张表本身就是"已启用集合"，
// 这个标志就没有意义了。
func kafkaConnections(cfg appkafka.Config) map[string]gofykafka.Config {
	out := make(map[string]gofykafka.Config, len(cfg))
	for name, inst := range cfg {
		if inst.Enabled {
			out[name] = inst.Config
		}
	}
	return out
}

// amqpConnections is the AMQP counterpart of kafkaConnections.
// amqpConnections 是 kafkaConnections 在 AMQP 上的对应实现。
func amqpConnections(cfg appamqp.Config) map[string]gofyamqp.Config {
	out := make(map[string]gofyamqp.Config, len(cfg))
	for name, inst := range cfg {
		if inst.Enabled {
			out[name] = inst.Config
		}
	}
	return out
}

// rocketMQConnections is the RocketMQ counterpart of kafkaConnections.
// rocketMQConnections 是 kafkaConnections 在 RocketMQ 上的对应实现。
func rocketMQConnections(cfg approcketmq.Config) map[string]gofyrocketmq.Config {
	out := make(map[string]gofyrocketmq.Config, len(cfg))
	for name, inst := range cfg {
		if inst.Enabled {
			out[name] = inst.Config
		}
	}
	return out
}

// redisConnections is the Redis counterpart of kafkaConnections.
// redisConnections 是 kafkaConnections 在 Redis 上的对应实现。
func redisConnections(cfg appredis.Config) map[string]gofyredis.Config {
	out := make(map[string]gofyredis.Config, len(cfg))
	for name, inst := range cfg {
		if inst.Enabled {
			out[name] = inst.Config
		}
	}
	return out
}
