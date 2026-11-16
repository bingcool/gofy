package group1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
	gofyworker "github.com/bingcool/gofy/support/worker"
)

// OrderBatchProducerConfig controls the burst that feeds the batch
// subscription: how many messages per tick, and how often.
//
// The fan-out producer sends one event to many transports; this one sends many
// events to one transport. That is the shape a batch consumer needs: a pull
// can only return several messages when several are waiting.
// OrderBatchProducerConfig 控制喂给批量订阅的突发消息：每次多少条、多久一次。
//
// 群发生产者把一条事件发给多个传输，这个生产者把多条事件发给一个传输。批量消费者需要的
// 正是这个形状：只有同时有多条消息在等，一次拉取才可能拿回多条。
type OrderBatchProducerConfig struct {
	// Concurrency is how many producer instances run inside the worker process.
	// Concurrency 是 worker 进程内运行的生产者实例数。
	Concurrency int
	// IntervalMS is the gap between two bursts, in milliseconds.
	// IntervalMS 是两次群发之间的间隔，单位毫秒。
	IntervalMS uint32
	// Burst is how many messages one tick sends. Keep it at or below the
	// subscription's BatchSize, otherwise every other pull fills with the
	// previous burst's tail and the log no longer shows one burst per batch.
	// Burst 是每次发送的条数。保持它不超过订阅的 BatchSize，否则每隔一次拉取装的就是上
	// 一批的尾巴，日志里也就看不出「一批一次突发」了。
	Burst int
	// OrderID is the order every message in the burst points at.
	// OrderID 是突发里每条消息指向的订单。
	OrderID uint64
	// UserID travels in the payload for audit; the update itself ignores it.
	// UserID 随 payload 一起走，供审计使用；更新本身并不需要它。
	UserID uint64
	// EventType selects the consumer branch, so it must be one applyOrderEvent
	// knows: order.paid or order.cancelled.
	// EventType 决定消费端的分支，因此必须是 applyOrderEvent 认识的值：
	// order.paid 或 order.cancelled。
	EventType string
}

// orderBatchProducerDefaults is what group1 runs with: ten messages every ten
// seconds, one batch per burst at the default BatchSize of ten.
// orderBatchProducerDefaults 是 group1 的运行参数：每十秒十条，按默认 BatchSize
// 十条计算正好一批。
var orderBatchProducerDefaults = OrderBatchProducerConfig{
	Concurrency: 1,
	IntervalMS:  10_000,
	Burst:       10,
	OrderID:     1,
	UserID:      1,
	EventType:   "order.paid",
}

// batchPublishTimeout bounds one burst, not one send. The first tick pays for
// the producer's route lookup and connection warm-up and can take seconds,
// which is why it is longer than the fan-out producer's per-tick budget: a
// timeout there would truncate the very burst the sample exists to show.
// batchPublishTimeout 限制一次突发，而不是一次发送。第一个 tick 要付生产者的路由查询与
// 连接预热成本，可能花上几秒，因此它比群发生产者的每 tick 预算更长：那边用的是同一套
// 预算，一旦超时就会截断本示例正要展示的那一批。
const batchPublishTimeout = 30 * time.Second

// newOrderBatchProducer publishes a burst of order events to the batch
// subscription's topic and tag every interval.
//
// Each message gets its own envelope id on purpose. The consumer's ledger is
// keyed by (consumer, message id), so reusing one id across the burst would
// turn nine of the ten into "message skipped" — the batch would still be pulled
// but only one message would reach the handler.
// newOrderBatchProducer 每间隔向批量订阅的 topic 与 tag 发一组订单事件。
//
// 每条消息刻意用各自的信封 ID。消费端台账按 (consumer, message id) 记账，如果整批共用
// 一个 ID，十条里有九条会变成「message skipped」——批次照样拉回来，但只有一条真正到达
// handler。
func newOrderBatchProducer(conns gofyconsumer.Connections, cfg OrderBatchProducerConfig) gofyworker.Worker {
	intervalMS := cfg.IntervalMS
	if intervalMS == 0 {
		intervalMS = 10_000
	}
	burst := cfg.Burst
	if burst < 1 {
		burst = 1
	}
	concurrency := cfg.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	return gofyworker.Func("order-batch-producer", uint8(concurrency), func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		if !ok {
			return errors.New("order-batch-producer: runtime is required")
		}
		id, _ := gofyworker.IdentityFrom(ctx)
		lg := zap.NewNop()
		if rt.Logger != nil {
			lg = rt.Logger.With(zap.String("worker", id.String()))
		}

		conn, ok := conns.RocketMQ[orderRocketMQBatch.Instance]
		if !ok {
			return fmt.Errorf("rocketmq instance %q is not configured", orderRocketMQBatch.Instance)
		}
		client, err := gofyrocketmq.New(ctx, conn)
		if err != nil {
			return fmt.Errorf("rocketmq instance %q: %w", orderRocketMQBatch.Instance, err)
		}
		defer func() { _ = client.Close() }()

		// The first tag is the one the batch consumer subscribes to; publishing
		// without it would leave the messages invisible to that subscription.
		// 第一个 tag 就是批量订阅收窄用的那个；不带它发布，消息对该订阅不可见。
		tag := ""
		if len(orderRocketMQBatch.Tags) > 0 && orderRocketMQBatch.Tags[0] != "*" {
			tag = orderRocketMQBatch.Tags[0]
		}

		lg.Info("order batch producer started",
			zap.Uint32("interval_ms", intervalMS),
			zap.Int("burst", burst),
			zap.String("topic", orderRocketMQBatch.Topic),
			zap.String("tag", tag),
		)
		defer lg.Info("order batch producer stopped")

		return gofyworker.Ticker(ctx, intervalMS, func(ctx context.Context) error {
			publishCtx, cancel := context.WithTimeout(gofyworker.DrainContext(ctx), batchPublishTimeout)
			defer cancel()

			// Sent one after another, not concurrently: the burst is meant to
			// land inside one long poll, and sequential sends already do that
			// within a few milliseconds while keeping the failure attributable
			// to a single message.
			// 顺序发送而不是并发：突发只要落在同一次长轮询里就够了，顺序发送几毫秒内就能做到，
			// 而且失败能归到具体某一条。
			occurredAt := time.Now()
			var errs []error
			for i := 1; i <= burst; i++ {
				payload, err := json.Marshal(orderEventPayload{
					OrderID: cfg.OrderID,
					UserID:  cfg.UserID,
					Reason:  fmt.Sprintf("记录来自 rocketmq 批量样例第 %d/%d 条", i, burst),
				})
				if err != nil {
					return fmt.Errorf("encode payload: %w", err)
				}
				_, err = client.PublishJSON(publishCtx, gofyconsumer.Envelope{
					ID:         uuid.NewString(),
					Type:       cfg.EventType,
					OccurredAt: occurredAt,
					Payload:    payload,
				}, gofyrocketmq.PublishOptions{
					Topic: orderRocketMQBatch.Topic,
					Tag:   tag,
					Keys:  []string{"order-" + strconv.FormatUint(cfg.OrderID, 10)},
				})
				if err != nil {
					// One slow or rejected send must not truncate the burst:
					// the messages already accepted are a batch of their own,
					// and the rest still deserve a chance to land.
					// 一次慢发送或被拒的发送不能截断整批：已发出的自成一批，剩下的仍该有机会
					// 落到 broker。
					errs = append(errs, fmt.Errorf("publish %d/%d: %w", i, burst, err))
				}
			}
			if len(errs) > 0 {
				return errors.Join(errs...)
			}
			lg.Info("order batch published",
				zap.Int("burst", burst),
				zap.Uint64("order_id", cfg.OrderID),
				zap.String("type", cfg.EventType),
			)
			return nil
		})
	})
}
