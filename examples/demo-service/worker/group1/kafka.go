package group1

import (
	"context"
	"errors"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofykafka "github.com/bingcool/gofy/support/kafka"
	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/bingcool/gofy/support/worker/consumer/kafkaconsumer"
)

// kafkaConsumerName is the worker name: it appears in `worker --list`, in log
// fields, in the pid file name and as the idempotency scope.
// kafkaConsumerName 是 worker 名：它出现在 `worker --list`、日志字段、pid 文件名
// 与幂等作用域里。
const kafkaConsumerName = "order-kafka-consumer"

// newKafkaConsumer adapts one Kafka subscription to a worker.
//
// The client is built inside Run instead of at registration time: `worker
// --list` and `--name` must not open a single broker connection, and a
// consumer that is filtered out should cost nothing.
// newKafkaConsumer 把一个 Kafka 订阅适配成 worker。
//
// 客户端在 Run 里建立，而不是注册时：`worker --list` 与 `--name` 不应该打开任何
// broker 连接，被过滤掉的消费者也不该产生成本。
func newKafkaConsumer(cfg gofyconsumer.KafkaConsumer, conn gofykafka.Config) gofyworker.Worker {
	return gofyworker.Func(kafkaConsumerName, gofyconsumer.Concurrency(cfg.Concurrency), func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		if !ok {
			return errors.New("order-kafka-consumer: worker runtime is missing from the context")
		}
		id, _ := gofyworker.IdentityFrom(ctx)
		lg := rt.Logger.With(
			zap.String("worker", id.String()),
			zap.String("consumer", kafkaConsumerName),
		)

		handler, err := newOrderHandler(rt)
		if err != nil {
			return err
		}
		proc, err := gofyconsumer.NewProcessor(rt, gofyconsumer.ProcessorConfig{
			Consumer:    kafkaConsumerName,
			Handler:     handler,
			Idempotency: cfg.Idempotency,
			MaxAttempts: cfg.MaxAttempts,
		})
		if err != nil {
			return err
		}

		client, err := gofykafka.New(ctx, conn)
		if err != nil {
			return fmt.Errorf("%s: connect: %w", kafkaConsumerName, err)
		}
		defer func() { _ = client.Close() }()

		consumer, err := gofykafka.NewConsumer(client, gofykafka.ConsumerConfig{
			Topic:           cfg.Topic,
			Group:           cfg.Group,
			CommitMode:      gofykafka.CommitMode(cfg.CommitMode),
			AutoOffsetReset: cfg.AutoOffsetReset,
			SessionTimeout:  cfg.SessionTimeout,
		})
		if err != nil {
			return fmt.Errorf("%s: subscribe %s: %w", kafkaConsumerName, cfg.Topic, err)
		}
		// The adapter closes the consumer on shutdown; this second close covers
		// the restart path, where the adapter deliberately leaves it open for
		// reuse. Here every restart rebuilds both, so the old one must go.
		// 适配器在停机时关闭消费者；这里的第二次关闭覆盖重启路径——适配器刻意不关
		// 是留给复用，而本实现每次重启都会重建，旧对象必须释放。
		defer func() { _ = consumer.Close() }()

		w, err := kafkaconsumer.New(kafkaConsumerName, consumer, kafkaconsumer.Config{
			Handler: func(ctx context.Context, record *kgo.Record) error {
				return proc.Handle(ctx, kafkaMessage(record))
			},
			Options: gofykafka.ConsumeOptions{
				DiscardOnError: cfg.DiscardOnError,
				MaxRecords:     cfg.MaxRecords,
				RetryDelay:     cfg.RetryDelay,
				OnError: func(err error) {
					lg.Error("kafka consume error", zap.Error(err))
				},
			},
		})
		if err != nil {
			return err
		}

		lg.Info("kafka consumer started",
			zap.String("topic", cfg.Topic),
			zap.String("group", cfg.Group),
			zap.String("commit_mode", cfg.CommitMode),
			zap.Bool("idempotency", cfg.Idempotency),
		)
		defer lg.Info("kafka consumer stopped", zap.String("topic", cfg.Topic))

		return w.Run(ctx)
	})
}

// kafkaMessage converts one Kafka record into the shared message view.
//
// Kafka does not report a delivery count, so Attempt stays at 1; the
// idempotency ledger, not the broker, is what bounds repeated handling here.
// kafkaMessage 把一条 Kafka 记录转成共用的消息视图。
//
// Kafka 不报告投递次数，因此 Attempt 固定为 1；在这里限制重复处理的是幂等台账，
// 而不是 broker。
func kafkaMessage(record *kgo.Record) gofyconsumer.Message {
	headers := make(map[string]string, len(record.Headers))
	for _, header := range record.Headers {
		headers[header.Key] = string(header.Value)
	}
	return gofyconsumer.Message{
		ID:       kafkaMessageID(record, headers),
		Source:   "kafka",
		Topic:    record.Topic,
		Key:      string(record.Key),
		Body:     record.Value,
		Attempt:  1,
		Headers:  headers,
		Enqueued: record.Timestamp,
	}
}

// kafkaMessageID prefers the producer's message id and falls back to the
// partition offset.
//
// The offset is stable for one record, so a redelivery of the same record
// deduplicates; a producer that re-sends the same business event under a new
// offset must set the message_id header for that to hold.
// kafkaMessageID 优先用生产者的 message id，缺失时回退到分区位点。
//
// 位点对同一条记录是稳定的，因此同一条记录的重投能去重；生产者若用新位点重发同一个
// 业务事件，就必须设置 message_id 头，去重才成立。
func kafkaMessageID(record *kgo.Record, headers map[string]string) string {
	if id := headers["message_id"]; id != "" {
		return id
	}
	return fmt.Sprintf("%s/%d@%d", record.Topic, record.Partition, record.Offset)
}
