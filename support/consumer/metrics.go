package consumer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Instrument names follow the OpenTelemetry dot convention; the Prometheus
// exporter turns dots into underscores and suffixes counters with _total.
// instrument 名沿用 OpenTelemetry 点号约定，Prometheus exporter 会把点转成下划线，
// 并给 counter 补上 _total。
const (
	metricReceived      = "gofy.consumer.received"
	metricAcked         = "gofy.consumer.acked"
	metricNacked        = "gofy.consumer.nacked"
	metricDead          = "gofy.consumer.dead"
	metricHandleSeconds = "gofy.consumer.handle.duration"
	metricLedgerErrors  = "gofy.consumer.ledger.errors"
	metricAckErrors     = "gofy.consumer.ack.errors"
	metricConnectErrors = "gofy.consumer.connect.errors"
	consumerMeterScope  = "github.com/bingcool/gofy/support/consumer"
)

// consumeDecision is the one acknowledgement decision for one delivery. Handle
// produces exactly one, so received == acked + nacked can be asserted on the
// scrape instead of inferred from logs.
// consumeDecision 是一条投递唯一的确认决定。Handle 只产生一次，因此可以直接在
// 抓取结果上断言 received == acked + nacked，而不是从日志里推断。
type consumeDecision struct {
	ack    bool
	reason string
	dead   bool
}

// consumerMetrics caches instruments once per Processor. With enabled == false
// every method returns before building attributes, so a metrics-free consumer
// pays nothing on the message path.
// consumerMetrics 为每个 Processor 缓存一次 instrument。enabled 为 false 时所有方法
// 都在构造标签前返回，没有指标的消费者在消息路径上零开销。
type consumerMetrics struct {
	enabled  bool
	consumer string
	topics   map[string]struct{}

	received      metric.Int64Counter
	acked         metric.Int64Counter
	nacked        metric.Int64Counter
	dead          metric.Int64Counter
	duration      metric.Float64Histogram
	ledgerErrors  metric.Int64Counter
	ackErrors     metric.Int64Counter
	connectErrors metric.Int64Counter
}

func newConsumerMetrics(provider metric.MeterProvider, enabled bool, consumer string, topics []string) (*consumerMetrics, error) {
	m := &consumerMetrics{consumer: consumer}
	if len(topics) > 0 {
		m.topics = make(map[string]struct{}, len(topics))
		for _, topic := range topics {
			if topic != "" {
				m.topics[topic] = struct{}{}
			}
		}
	}
	// Metrics=true with a nil provider can only be a wiring mistake — no valid
	// configuration looks like this — so reject it instead of silently
	// running without metrics.
	// Metrics=true 且 provider 为 nil 只可能是接线错误——没有任何合法配置长这样
	// ——因此直接拒绝，而不是静默地跑成无指标。
	if enabled && provider == nil {
		return nil, errors.New("consumer: Metrics is true but MeterProvider is nil; set both fields together")
	}
	if !enabled {
		return m, nil
	}
	meter := provider.Meter(consumerMeterScope)
	var err error
	if m.received, err = meter.Int64Counter(metricReceived,
		metric.WithDescription("Deliveries that entered the consume chain.")); err != nil {
		return nil, fmt.Errorf("consumer: create %s: %w", metricReceived, err)
	}
	if m.acked, err = meter.Int64Counter(metricAcked,
		metric.WithDescription("Deliveries acknowledged by reason.")); err != nil {
		return nil, fmt.Errorf("consumer: create %s: %w", metricAcked, err)
	}
	if m.nacked, err = meter.Int64Counter(metricNacked,
		metric.WithDescription("Deliveries left for redelivery by reason.")); err != nil {
		return nil, fmt.Errorf("consumer: create %s: %w", metricNacked, err)
	}
	if m.dead, err = meter.Int64Counter(metricDead,
		metric.WithDescription("Messages parked as dead.")); err != nil {
		return nil, fmt.Errorf("consumer: create %s: %w", metricDead, err)
	}
	if m.duration, err = meter.Float64Histogram(metricHandleSeconds,
		metric.WithUnit("s"),
		metric.WithDescription("Duration of the consume chain, excluding the broker acknowledgement.")); err != nil {
		return nil, fmt.Errorf("consumer: create %s: %w", metricHandleSeconds, err)
	}
	if m.ledgerErrors, err = meter.Int64Counter(metricLedgerErrors,
		metric.WithDescription("Ledger writes that failed after an acknowledgement decision.")); err != nil {
		return nil, fmt.Errorf("consumer: create %s: %w", metricLedgerErrors, err)
	}
	if m.ackErrors, err = meter.Int64Counter(metricAckErrors,
		metric.WithDescription("Broker acknowledgement failures.")); err != nil {
		return nil, fmt.Errorf("consumer: create %s: %w", metricAckErrors, err)
	}
	if m.connectErrors, err = meter.Int64Counter(metricConnectErrors,
		metric.WithDescription("Broker connection and subscription failures.")); err != nil {
		return nil, fmt.Errorf("consumer: create %s: %w", metricConnectErrors, err)
	}
	m.enabled = true
	return m, nil
}

// labelTopic keeps the topic label bounded by configured values. An AMQP
// routing key that does not match the configured binding is just as unexpected
// as an arbitrary string, and must not create a new time series.
// labelTopic 把 topic 标签限制在配置值内。AMQP routing key 若与配置的 binding 不符，
// 与任意字符串一样意外，不能因此造出新时间线。
func (m *consumerMetrics) labelTopic(topic string) string {
	// An empty whitelist has no configured value, so nothing can match and
	// every topic collapses to "unknown". The passthrough branch that used to
	// live here turned one rogue delivery into unbounded series.
	// 空白名单没有任何配置值，因此什么都不可能匹配，所有 topic 一律收敛为
	// "unknown"。原先这里的透传分支会让一条意外消息造出无限序列。
	if _, ok := m.topics[topic]; ok {
		return topic
	}
	return "unknown"
}

func labelBroker(source string) string {
	switch source {
	case "kafka", "amqp", "rocketmq", "queues":
		return source
	default:
		return "unknown"
	}
}

// record is called once from Handle's deferred wrapper. Duration uses the
// fixed six-value outcome set; a ledger claim failure is a retry as far as the
// handler is concerned, and gets its own ledger.error series.
// record 由 Handle 的 defer 包装调用一次。耗时使用固定的六个 outcome；
// 台账认领失败从 handler 角度看属于 retry，同时另有 ledger.error 序列。
func (m *consumerMetrics) record(msg Message, decision consumeDecision, elapsed time.Duration) {
	if !m.enabled {
		return
	}
	broker := labelBroker(msg.Source)
	topic := m.labelTopic(msg.Topic)
	m.received.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("consumer", m.consumer),
		attribute.String("topic", topic),
		attribute.String("broker", broker),
	))
	attrs := []attribute.KeyValue{
		attribute.String("consumer", m.consumer),
		attribute.String("topic", topic),
		attribute.String("broker", broker),
		attribute.String("reason", decision.reason),
	}
	if decision.ack {
		m.acked.Add(context.Background(), 1, metric.WithAttributes(attrs...))
	} else {
		m.nacked.Add(context.Background(), 1, metric.WithAttributes(attrs...))
	}
	if decision.dead {
		m.dead.Add(context.Background(), 1, metric.WithAttributes(attrs...))
	}
	outcome := decision.reason
	if outcome == "ledger" {
		outcome = "retry"
	}
	m.duration.Record(context.Background(), elapsed.Seconds(), metric.WithAttributes(
		attribute.String("consumer", m.consumer),
		attribute.String("topic", topic),
		attribute.String("outcome", outcome),
	))
}

func (m *consumerMetrics) recordLedger(stage string) {
	if !m.enabled {
		return
	}
	m.ledgerErrors.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("consumer", m.consumer),
		attribute.String("stage", stage),
	))
}

func (m *consumerMetrics) recordBrokerError(broker, op string) {
	if !m.enabled {
		return
	}
	m.ackErrors.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("broker", labelBroker(broker)),
		attribute.String("op", op),
	))
}

func (m *consumerMetrics) recordConnectError(broker, reason string) {
	if !m.enabled {
		return
	}
	m.connectErrors.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("broker", labelBroker(broker)),
		attribute.String("reason", reason),
	))
}
