// Package group1 holds the first worker group: the fast work that scales with
// order volume.
//
// The group hosts the whole produce → consume path. The scan loop polls on a
// timer and needs nothing but the io-db generation the process assembled. Six immediate
// subscriptions cover Kafka twice (the default one and a manual-commit demo),
// RabbitMQ, RocketMQ twice (one message per pull, and a batch pull fed by its
// own burst producer) and a Redis queue; each one is a file of its own holding
// its settings and its handler, and nothing else — the chain behind them
// (envelope, idempotency ledger, trace, error classification) is
// support/consumer. The producer fans the same order event out to every one of
// those sinks on a timer, closing the immediate loop inside one process. The
// delayed half mirrors it: a scheduler writes due-dated events to a Redis ZSET,
// a RocketMQ DELAY topic and two RabbitMQ TTL + dead-letter workflows (direct
// and topic), and four more subscriptions consume them. Beside the transports
// sits the database queue: a timer enqueues rows into a MySQL job table and two
// claim consumers take them with row-level leases, which is the shape a scan
// loop needs when several replicas must not repeat each other's work.
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
// 分组里跑着完整的 生产 → 消费 链路。扫描循环按定时器轮询，只依赖本进程装配的 io-db 代际。
// 六个立即订阅覆盖 Kafka 两次（默认订阅与手动提交样例）、RabbitMQ、RocketMQ 两次
// （一次一条与一次一批，后者由自己的突发生产者喂养）与 Redis 队列；每个消费者一个文件，
// 里面只有它自己的设置与 handler，没有别的——它们背后的信封、
// 幂等台账、trace 与错误分类都在 support/consumer。生产者按定时器把同一个订单事件群发到
// 每一个 sink，让立即链路的闭环跑在一个进程里。延迟链路是它的镜像：调度器把带到期时间的
// 事件写进 Redis ZSET、RocketMQ DELAY topic 与 RabbitMQ 的 direct / topic 两套 TTL +
// 死信队列，另外四个订阅负责消费。传输层旁边还有数据库队列：定时器把行写进 MySQL 任务表，
// 两个认领消费者用行级租约取走它们——多副本不能重复干同一件事时，扫描循环要的就是这个形状。
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

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofyworker "github.com/bingcool/gofy/support/worker"
)

// NewGroup builds group1: the scan loop, the immediate and delayed
// subscriptions, the four producers, the claim queue pair and the liveness
// heartbeat.
//
// Every consumer is registered unconditionally — that is what "one file per
// subscription" means. Which ones actually run is decided at startup by
// `worker --name`; none of them connects before Run, so `--list` and a filtered
// worker cost nothing. A consumer whose broker instance is missing from the
// application config fails here, before any goroutine exists.
//
// The four producers (producer.go, rocketmq_batch_producer.go,
// delay_producer.go, scan_claim.go) complete the loop: every interval the
// fan-out one publishes the same order event to every immediate sink, the burst
// one publishes a batch's worth of messages to the batch subscription, and the
// settle producer enqueues a batch of database jobs, so one process demonstrates
// produce → consume end to end without a second terminal.
// NewGroup 构造 group1：扫描循环、立即与延迟订阅、四个生产者、认领队列的一对 worker 与
// 存活心跳。
//
// 每个消费者都无条件注册——这正是"一个订阅一个文件"的含义。实际运行哪些由启动时的
// `worker --name` 决定；它们在 Run 之前都不建连，因此 `--list` 与被过滤掉的 worker
// 不产生成本。消费者引用的 broker 实例在应用配置里缺失时，会在这一步失败，早于任何
// goroutine 存在。
//
// 四个生产者（producer.go、rocketmq_batch_producer.go、delay_producer.go、
// scan_claim.go）补全了闭环：群发的那个每个间隔把同一个订单事件发往所有立即 sink，突发的
// 那个按批把消息发给批量订阅，入队的那个每轮写一批数据库任务；一个进程即可演示
// 生产 → 消费 的完整链路，无需第二个终端。
// The tenant plugin is not installed here. It is installed when the worker
// process assembles its IO generation (appio.NewDBOnlyModuleFactory with the
// tenant options), which happens before this group starts — so by the time any
// worker runs, the pool is already filtered. A group therefore takes no tenant
// section: it has no assembly point to apply it to.
// 租户插件不在这里安装。它在 worker 进程装配 IO 代际时装好（appio.NewDBOnlyModuleFactory
// 带上租户选项），位置早于本分组启动——因此任何 worker 运行时，池已经带上过滤。分组
// 因此不再接收租户段：它没有可施加该选项的装配点。
func NewGroup(conns gofyconsumer.Connections) (*gofyworker.Group, error) {
	workers := []gofyworker.Worker{
		newScanWorker(scanDefaults),
		newOrderEventProducer(conns, orderEventProducerDefaults),
		newOrderBatchProducer(conns, orderBatchProducerDefaults),
		newDelayEventProducer(conns, delayEventProducerDefaults),
		newClaimScanProducer(claimScanDefaults),
	}

	kafka, err := gofyconsumer.NewKafka(conns, orderKafka)
	if err != nil {
		return nil, fmt.Errorf("group1: %w", err)
	}
	kafkaManual, err := gofyconsumer.NewKafka(conns, orderKafkaManual)
	if err != nil {
		return nil, fmt.Errorf("group1: %w", err)
	}
	amqp, err := gofyconsumer.NewAMQP(conns, orderAMQP)
	if err != nil {
		return nil, fmt.Errorf("group1: %w", err)
	}
	rocketmq, err := gofyconsumer.NewRocketMQ(conns, orderRocketMQ)
	if err != nil {
		return nil, fmt.Errorf("group1: %w", err)
	}
	rocketmqBatch, err := gofyconsumer.NewRocketMQ(conns, orderRocketMQBatch)
	if err != nil {
		return nil, fmt.Errorf("group1: %w", err)
	}
	queues, err := gofyconsumer.NewQueues(conns, orderQueues)
	if err != nil {
		return nil, fmt.Errorf("group1: %w", err)
	}
	delayQueues, err := gofyconsumer.NewDelayQueues(conns, orderDelayQueues)
	if err != nil {
		return nil, fmt.Errorf("group1: %w", err)
	}
	rocketmqDelay, err := gofyconsumer.NewRocketMQ(conns, orderRocketMQDelay)
	if err != nil {
		return nil, fmt.Errorf("group1: %w", err)
	}
	amqpDelay, err := gofyconsumer.NewDelayAMQP(conns, orderAMQPDelay)
	if err != nil {
		return nil, fmt.Errorf("group1: %w", err)
	}
	amqpDelayTopic, err := gofyconsumer.NewDelayAMQP(conns, orderAMQPDelayTopic)
	if err != nil {
		return nil, fmt.Errorf("group1: %w", err)
	}

	workers = append(workers,
		kafka, kafkaManual, amqp, rocketmq, rocketmqBatch, queues,
		delayQueues, rocketmqDelay, amqpDelay, amqpDelayTopic,
		newClaimScanConsumer(claimScanDefaults),
		newHeartbeatWorker(),
	)
	return gofyworker.NewGroup("group1", workers...)
}
