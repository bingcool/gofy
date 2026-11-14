package consumer

import (
	"fmt"

	gofyamqp "github.com/bingcool/gofy/support/amqp"
	gofykafka "github.com/bingcool/gofy/support/kafka"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
)

// Connections is the set of already-decoded broker configurations a consumer
// can name. The application fills it from the connection blocks it already
// decodes — kafka, amqp, rocketmq, redis — and each consumer picks its instance
// by name.
//
// Passing the whole map instead of a single config keeps the resolution — and
// the "instance not configured" error — at registration time, before any
// consumer goroutine exists. Registration does not connect: Run does, so
// `worker --list` and a worker that is filtered out cost nothing.
// Connections 是消费者可以按名字引用的 broker 配置集合。应用用自己已经解码的连接段
// 填充它——kafka、amqp、rocketmq、redis——每个消费者按名字挑自己的那个。
//
// 传整张表而不是单个配置，是为了把解析和"实例未配置"的错误留在注册阶段，那时还没有
// 任何消费者 goroutine。注册不建连：建连在 Run 里，因此 `worker --list` 与被过滤掉的
// worker 都不产生成本。
type Connections struct {
	Kafka    map[string]gofykafka.Config
	AMQP     map[string]gofyamqp.Config
	RocketMQ map[string]gofyrocketmq.Config
	Redis    map[string]gofyredis.Config
}

// ResolveKafka returns the named Kafka connection.
// ResolveKafka 返回指定名字的 Kafka 连接配置。
func (c Connections) ResolveKafka(name string) (gofykafka.Config, error) {
	cfg, ok := c.Kafka[name]
	if !ok {
		return gofykafka.Config{}, fmt.Errorf("kafka instance %q is not configured", name)
	}
	return cfg, nil
}

// ResolveAMQP returns the named AMQP connection.
// ResolveAMQP 返回指定名字的 AMQP 连接配置。
func (c Connections) ResolveAMQP(name string) (gofyamqp.Config, error) {
	cfg, ok := c.AMQP[name]
	if !ok {
		return gofyamqp.Config{}, fmt.Errorf("amqp instance %q is not configured", name)
	}
	return cfg, nil
}

// ResolveRocketMQ returns the named RocketMQ connection.
// ResolveRocketMQ 返回指定名字的 RocketMQ 连接配置。
func (c Connections) ResolveRocketMQ(name string) (gofyrocketmq.Config, error) {
	cfg, ok := c.RocketMQ[name]
	if !ok {
		return gofyrocketmq.Config{}, fmt.Errorf("rocketmq instance %q is not configured", name)
	}
	return cfg, nil
}

// ResolveRedis returns the named Redis connection.
// ResolveRedis 返回指定名字的 Redis 连接配置。
func (c Connections) ResolveRedis(name string) (gofyredis.Config, error) {
	cfg, ok := c.Redis[name]
	if !ok {
		return gofyredis.Config{}, fmt.Errorf("redis instance %q is not configured", name)
	}
	return cfg, nil
}
