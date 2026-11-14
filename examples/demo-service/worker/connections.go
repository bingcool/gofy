package worker

import (
	gofyamqp "github.com/bingcool/gofy/support/amqp"
	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofykafka "github.com/bingcool/gofy/support/kafka"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"

	appamqp "github.com/yourorg/demo-service/app/appio/amqp"
	appkafka "github.com/yourorg/demo-service/app/appio/kafka"
	appredis "github.com/yourorg/demo-service/app/appio/redis"
	approcketmq "github.com/yourorg/demo-service/app/appio/rocketmq"
	"github.com/yourorg/demo-service/app/config"
)

// ConnectionsFrom builds the broker table that every subscription in group1
// names with Instance / RedisInstance.
//
// The input is the configuration the application already decoded, so an address
// has exactly one home: config.yaml. A consumer says "default" or "ratelimit"
// and never repeats a host, a port or a password; moving a cluster is a config
// change, not a code change.
//
// Two things deliberately do not happen here. Nothing connects — the table is
// only data, and Register turns a name into a concrete config, so `worker
// --list` and a `--name`-filtered worker open no socket. And nothing is
// filtered: `enabled: false` blocks are kept because that flag answers whether
// the HTTP process opens the connection at startup. A worker decides by whether
// a subscription was registered, so a disabled Kafka producer and an enabled
// Kafka consumer of the same block are both legitimate.
// ConnectionsFrom 构造 group1 里每个订阅用 Instance / RedisInstance 引用的 broker
// 连接表。
//
// 输入是应用已经解码过的配置，因此一个地址只有一个家：config.yaml。消费者只说
// "default" 或 "ratelimit"，不重复 host、端口或密码；搬集群是改配置，不是改代码。
//
// 有两件事刻意不在这里发生。其一是不建连——这张表只是数据，Register 把名字解析成具体
// 配置，因此 `worker --list` 与被 --name 过滤掉的 worker 一个 socket 都不开。其二是不
// 过滤：`enabled: false` 的块原样保留，因为那个标志回答的是"HTTP 进程启动时是否打开这条
// 连接"。worker 的决定权是"有没有注册这个订阅"，所以同一个块上关掉 Kafka 生产者、同时
// 消费 Kafka 是完全正当的。
func ConnectionsFrom(cfg *config.Config) gofyconsumer.Connections {
	return gofyconsumer.Connections{
		Kafka:    kafkaConnections(cfg.Kafka),
		AMQP:     amqpConnections(cfg.AMQP),
		RocketMQ: rocketMQConnections(cfg.RocketMQ),
		Redis:    redisConnections(cfg.Redis),
	}
}

// kafkaConnections copies the Kafka blocks into the bare form a consumer wants.
//
// appkafka.Instance is gofykafka.Config plus an Enabled flag. The flag has
// already done its job in the HTTP process, so only the connection settings
// travel on; keeping the wrapper would force support/consumer to import this
// application's io package and to depend on a decision that is not its own.
//
// The result is a new map with copied values, so neither side can mutate the
// other's configuration by accident.
//
// The four helpers below are near-identical on purpose: the embedded Config
// field is not reachable through a type parameter without an interface, and an
// interface to express a three-line loop would be longer than the loop.
// kafkaConnections 把 Kafka 段复制成消费者需要的裸配置。
//
// appkafka.Instance 是 gofykafka.Config 加一个 Enabled 标志。这个标志在 HTTP 进程里
// 已经用过了，因此只有连接参数继续往下走；保留包装类型会逼着 support/consumer 导入本应用
// 的 io 包，并依赖一个不属于它的决定。
//
// 返回的是新 map、值也是复制出来的，因此两边都不会误改对方的配置。
//
// 下面四个函数几乎一样是故意的：不借助接口就拿不到内嵌的 Config 字段，而为了三行循环
// 定义一层接口，比这三行还长。
func kafkaConnections(cfg appkafka.Config) map[string]gofykafka.Config {
	out := make(map[string]gofykafka.Config, len(cfg))
	for name, inst := range cfg {
		out[name] = inst.Config
	}
	return out
}

// amqpConnections is kafkaConnections for AMQP: hosts, credentials, vhost and
// the connection timeouts, in the framework's own Config type.
// amqpConnections 是 AMQP 版的 kafkaConnections：hosts、认证、vhost 与连接超时，
// 类型是框架自己的 Config。
func amqpConnections(cfg appamqp.Config) map[string]gofyamqp.Config {
	out := make(map[string]gofyamqp.Config, len(cfg))
	for name, inst := range cfg {
		out[name] = inst.Config
	}
	return out
}

// rocketMQConnections is kafkaConnections for RocketMQ. Only the endpoint and
// the ACL settings travel: topic, group and tags belong to a subscription, so
// one instance can serve several consumers with different groups.
// rocketMQConnections 是 RocketMQ 版的 kafkaConnections。传递的只有 endpoint 与 ACL；
// topic、group 与 tags 属于订阅，因此同一个实例可以服务多个不同 group 的消费者。
func rocketMQConnections(cfg approcketmq.Config) map[string]gofyrocketmq.Config {
	out := make(map[string]gofyrocketmq.Config, len(cfg))
	for name, inst := range cfg {
		out[name] = inst.Config
	}
	return out
}

// redisConnections is kafkaConnections for Redis. The demo's queue consumer
// names "ratelimit" — the pool the HTTP process already reuses for throttling —
// so the worker shares that address rather than asking for a second one.
// redisConnections 是 Redis 版的 kafkaConnections。示例的队列消费者引用 "ratelimit"
// ——HTTP 进程已经为限流复用的那条连接池——因此 worker 共用这个地址，而不是再要一份。
func redisConnections(cfg appredis.Config) map[string]gofyredis.Config {
	out := make(map[string]gofyredis.Config, len(cfg))
	for name, inst := range cfg {
		out[name] = inst.Config
	}
	return out
}
