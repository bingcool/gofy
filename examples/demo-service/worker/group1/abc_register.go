// Package group1 holds the first worker group: the fast work that scales with
// order volume.
//
// The group hosts two shapes of work. The scan loop polls on a timer and needs
// nothing but the runtime database; the four consumers subscribe to Kafka,
// RabbitMQ, RocketMQ and a Redis queue, share one handling chain (envelope,
// idempotency ledger, trace, error classification) and differ only in how they
// turn a protocol record into a message.
//
// The package boundary is the deployment boundary: group1 is started by
// `demo-service worker --group=group1` and owns its own settings, so a change
// here never reaches group2.
//
// The file is named abc_register.go so the group's entry point sorts above its
// member files: "a" beats heartbeat.go and scan.go, whereas a plain register.go
// sinks below both. The prefix has to be a real character, not an underscore —
// Go ignores files starting with "_" or ".", so `_register.go` would leave the
// package without its NewGroup.
//
// 包 group1 是第一个 worker 分组：随订单量扩缩的秒级任务。
//
// 分组里有两种形态的任务。扫描循环按定时器轮询，只依赖 Runtime 的数据库；四个消费者
// 分别订阅 Kafka、RabbitMQ、RocketMQ 与 Redis 队列，共用同一条处理链路（信封、幂等
// 台账、trace、错误分类），差异只在"如何把协议记录变成一条消息"。
//
// 包边界就是部署边界：group1 由 `demo-service worker --group=group1` 启动，参数也归
// 它自己，因此改动这里不会波及 group2。
//
// 文件名是 abc_register.go，让分组的入口排在成员文件之前："a" 在 heartbeat.go、
// scan.go 前面，而裸的 register.go 会沉到两者之后。前缀必须是真实字符而不是下划线——
// Go 忽略以 "_" 或 "." 开头的文件，`_register.go` 会让这个包丢掉 NewGroup。
package group1

import (
	"fmt"

	gofyworker "github.com/bingcool/gofy/support/worker"
)

// NewGroup builds group1: the scan loop, the four consumers the configuration
// enables and the liveness heartbeat.
//
// Only enabled consumers are registered, so `worker --list` shows what will
// actually run and a disabled broker produces neither a worker nor a
// connection. The heartbeat stays last: its job is to prove the process is
// alive after everything else has started.
// NewGroup 构造 group1：扫描循环、配置启用的四个消费者与存活心跳。
//
// 只有启用的消费者会被注册，因此 `worker --list` 显示的就是实际会运行的内容，未
// 启用的 broker 既不会产生 worker 也不会建连。心跳留在最后：它的职责是证明进程在
// 其余部分都启动之后仍然活着。
func NewGroup(cfg Config, conns Connections) (*gofyworker.Group, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	workers := []gofyworker.Worker{newScanWorker(scanDefaults)}

	if consumer := cfg.Consumers.Kafka; consumer.Enabled {
		conn, err := conns.kafka(consumer.Instance)
		if err != nil {
			return nil, fmt.Errorf("group1: kafka consumer: %w", err)
		}
		workers = append(workers, newKafkaConsumer(consumer, conn))
	}
	if consumer := cfg.Consumers.AMQP; consumer.Enabled {
		conn, err := conns.amqp(consumer.Instance)
		if err != nil {
			return nil, fmt.Errorf("group1: amqp consumer: %w", err)
		}
		workers = append(workers, newAMQPConsumer(consumer, conn))
	}
	if consumer := cfg.Consumers.RocketMQ; consumer.Enabled {
		conn, err := conns.rocketmq(consumer.Instance)
		if err != nil {
			return nil, fmt.Errorf("group1: rocketmq consumer: %w", err)
		}
		workers = append(workers, newRocketMQConsumer(consumer, conn))
	}
	if consumer := cfg.Consumers.Queues; consumer.Enabled {
		conn, err := conns.redis(consumer.RedisInstance)
		if err != nil {
			return nil, fmt.Errorf("group1: queues consumer: %w", err)
		}
		workers = append(workers, newQueuesConsumer(consumer, conn))
	}

	workers = append(workers, newHeartbeatWorker())
	return gofyworker.NewGroup("group1", workers...)
}
