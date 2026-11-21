package consumer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/support/otel"
	"go.uber.org/zap"
)

// TestConsumerMetricsInvariantAndCardinality is the Phase 3 acceptance test:
// 1000 deliveries with mixed outcomes must produce one acknowledgement decision
// each, and a second 1000 must not create another time series.
// TestConsumerMetricsInvariantAndCardinality 是 Phase 3 的验收用例：1000 条混合结果的
// 投递必须每条恰好一个确认决定，再来 1000 条不能造出新的时间线。
func TestConsumerMetricsInvariantAndCardinality(t *testing.T) {
	metrics, err := otel.NewMetrics(otel.MetricsConfig{Enabled: true}, otel.WithServiceName("consumer-metrics-test"))
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = metrics.Shutdown(context.Background()) }()

	rt := core.NewRuntime(core.RuntimeConfig{
		Logger:        zap.NewNop(),
		MeterProvider: metrics.MeterProvider(),
		Metrics:       true,
	})
	var handlerErr error
	proc, err := NewProcessor(context.Background(), rt, ProcessorConfig{
		Consumer:      "orders-consumer",
		Handler:       func(context.Context, Message, Envelope) error { return handlerErr },
		MaxAttempts:   5,
		MeterProvider: metrics.MeterProvider(),
		Metrics:       true,
		Topics:        []string{"orders"},
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}

	run := func(start, count int) {
		for i := start; i < start+count; i++ {
			handlerErr = nil
			body := []byte(`{"type":"order.paid","payload":{"order_id":1}}`)
			switch i % 5 {
			case 0:
			case 1:
				handlerErr = Permanent(errors.New("bad event"))
			case 2:
				handlerErr = errors.New("try later")
			case 3:
				handlerErr = ErrInFlight
			case 4:
				body = []byte("not json")
			}
			source := []string{"kafka", "amqp", "rocketmq", "queues", "bogus"}[i%5]
			topic := "orders"
			if i%10 == 9 {
				topic = "rogue-topic" // outside the whitelist
			}
			_ = proc.Handle(context.Background(), Message{
				ID:     fmt.Sprintf("m-%d", i),
				Source: source,
				Topic:  topic,
				Body:   body,
			})
		}
	}

	run(0, 1000)
	body := scrapeConsumerMetrics(t, metrics)
	sum := func(name string) float64 {
		return consumerSum(body, name, "")
	}
	if got := sum("gofy_consumer_received_total"); got != 1000 {
		t.Fatalf("received = %g, want 1000\n%s", got, body)
	}
	acked := sum("gofy_consumer_acked_total")
	nacked := sum("gofy_consumer_nacked_total")
	if acked+nacked != 1000 {
		t.Fatalf("acked %g + nacked %g = %g, want 1000\n%s", acked, nacked, acked+nacked, body)
	}
	dead := sum("gofy_consumer_dead_total")
	parkedAcks := consumerSum(body, "gofy_consumer_acked_total", `reason="permanent"`) +
		consumerSum(body, "gofy_consumer_acked_total", `reason="decode"`)
	if dead > parkedAcks {
		t.Fatalf("dead %g exceeds parked ACKs %g\n%s", dead, parkedAcks, body)
	}
	if !strings.Contains(body, `topic="unknown"`) || strings.Contains(body, `topic="rogue-topic"`) {
		t.Fatalf("topic whitelist was not applied\n%s", body)
	}
	if !strings.Contains(body, `broker="unknown"`) {
		t.Fatalf("broker whitelist was not applied\n%s", body)
	}

	// The hook-backed instruments use fixed labels, so the error helpers can be
	// checked on the same scrape without a live broker.
	proc.metrics.recordLedger("done")
	proc.metrics.recordBrokerError("kafka", "ack")
	proc.metrics.recordConnectError("amqp", "subscribe")
	body = scrapeConsumerMetrics(t, metrics)
	first := consumerSeries(body)
	for name, labels := range map[string][]string{
		"gofy_consumer_ledger_errors_total":  {`consumer="orders-consumer"`, `stage="done"`},
		"gofy_consumer_ack_errors_total":     {`broker="kafka"`, `op="ack"`},
		"gofy_consumer_connect_errors_total": {`broker="amqp"`, `reason="subscribe"`},
	} {
		if !containsSeries(body, name, labels...) {
			t.Fatalf("scrape is missing series %s %v\n%s", name, labels, body)
		}
	}

	run(1000, 1000)
	second := consumerSeries(scrapeConsumerMetrics(t, metrics))
	if second != first {
		t.Fatalf("series count grew from %d to %d after another 1000 messages", first, second)
	}
}

// TestConsumerMetricsCountPanicDelivery is the M-01 regression test: a handler
// panic escapes Handle (the broker layers recover it and ask for redelivery),
// but the delivery must still be counted as nacked with the retry reason.
// TestConsumerMetricsCountPanicDelivery 是 M-01 的回归用例：handler panic 会逃出
// Handle（broker 层 recover 后请求重投），但这次投递必须按 retry 记入 nack。
func TestConsumerMetricsCountPanicDelivery(t *testing.T) {
	metrics, err := otel.NewMetrics(otel.MetricsConfig{Enabled: true}, otel.WithServiceName("consumer-panic-test"))
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = metrics.Shutdown(context.Background()) }()

	rt := core.NewRuntime(core.RuntimeConfig{
		Logger:        zap.NewNop(),
		MeterProvider: metrics.MeterProvider(),
		Metrics:       true,
	})
	proc, err := NewProcessor(context.Background(), rt, ProcessorConfig{
		Consumer: "orders-consumer",
		Handler: func(context.Context, Message, Envelope) error {
			panic("handler exploded")
		},
		MaxAttempts:   5,
		MeterProvider: metrics.MeterProvider(),
		Metrics:       true,
		Topics:        []string{"orders"},
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("handler panic did not escape Handle")
			}
		}()
		_ = proc.Handle(context.Background(), Message{
			ID:     "m-panic",
			Source: "kafka",
			Topic:  "orders",
			Body:   []byte(`{"type":"order.paid","payload":{"order_id":1}}`),
		})
	}()

	body := scrapeConsumerMetrics(t, metrics)
	if got := consumerSum(body, "gofy_consumer_received_total", ""); got != 1 {
		t.Fatalf("received = %g, want 1\n%s", got, body)
	}
	if got := consumerSum(body, "gofy_consumer_nacked_total", `reason="retry"`); got != 1 {
		t.Fatalf("nacked{reason=retry} = %g, want 1\n%s", got, body)
	}
	if got := consumerSum(body, "gofy_consumer_acked_total", ""); got != 0 {
		t.Fatalf("acked = %g, want 0\n%s", got, body)
	}
	if !containsSeries(body, "gofy_consumer_handle_duration_seconds_count") {
		t.Fatalf("handle duration histogram is missing\n%s", body)
	}
}

func TestDisabledConsumerMetricsDoNotAllocate(t *testing.T) {
	m := &consumerMetrics{
		consumer: "orders-consumer",
		topics:   map[string]struct{}{"orders": {}},
	}
	msg := Message{ID: "m-1", Source: "kafka", Topic: "orders", Body: []byte("{}")}
	decision := consumeDecision{ack: true, reason: "success"}
	allocs := testing.AllocsPerRun(200, func() {
		m.record(msg, decision, 1)
		m.recordLedger("done")
		m.recordBrokerError("kafka", "ack")
		m.recordConnectError("kafka", "connect")
	})
	if allocs != 0 {
		t.Fatalf("disabled metrics allocated %g times per call", allocs)
	}
}

func scrapeConsumerMetrics(t *testing.T, m *otel.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, otel.DefaultMetricsPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// consumerSeries counts samples, ignoring HELP/TYPE lines. The count is the
// cardinality proxy: a new label value would add a sample without changing the
// message mix.
// consumerSeries 统计样本数，忽略 HELP/TYPE 行。它作为基数代理：新标签值会在
// 消息组合不变的情况下多出一条样本。
func consumerSeries(body string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "gofy_consumer_") {
			n++
		}
	}
	return n
}

func containsSeries(body, name string, labels ...string) bool {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+"{") {
			continue
		}
		matched := true
		for _, label := range labels {
			if !strings.Contains(line, label) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func consumerSum(body, name, label string) float64 {
	var total float64
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+"{") || (label != "" && !strings.Contains(line, label)) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		total += value
	}
	return total
}

// TestNewProcessorRejectsMetricsWithoutProvider is the M-06 regression test:
// the switch without a provider can only be a wiring mistake.
// TestNewProcessorRejectsMetricsWithoutProvider 是 M-06 的回归用例：开关配 nil
// provider 只可能是接线错误。
func TestNewProcessorRejectsMetricsWithoutProvider(t *testing.T) {
	rt := core.NewRuntime(core.RuntimeConfig{Logger: zap.NewNop()})
	_, err := NewProcessor(context.Background(), rt, ProcessorConfig{
		Consumer: "orders-consumer",
		Handler:  func(context.Context, Message, Envelope) error { return nil },
		Metrics:  true,
	})
	if err == nil {
		t.Fatal("NewProcessor with Metrics=true and a nil provider must fail")
	} else if !strings.Contains(err.Error(), "set both fields together") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestLabelTopicCollapsesWithoutWhitelist is the M-07 regression test: an empty
// whitelist has no configured value, so every topic — including rogue ones —
// collapses to "unknown" instead of passing through.
// TestLabelTopicCollapsesWithoutWhitelist 是 M-07 的回归用例：空白名单没有任何
// 配置值，因此所有 topic——包括意外值——都收敛为 "unknown"，不再透传。
func TestLabelTopicCollapsesWithoutWhitelist(t *testing.T) {
	m := &consumerMetrics{}
	for _, topic := range []string{"", "orders", "rogue-topic"} {
		if got := m.labelTopic(topic); got != "unknown" {
			t.Fatalf("labelTopic(%q) = %q, want unknown with an empty whitelist", topic, got)
		}
	}
	m2 := &consumerMetrics{topics: map[string]struct{}{"orders": {}}}
	if got := m2.labelTopic("orders"); got != "orders" {
		t.Fatalf("labelTopic(orders) = %q, want orders with a configured whitelist", got)
	}
	if got := m2.labelTopic("rogue-topic"); got != "unknown" {
		t.Fatalf("labelTopic(rogue-topic) = %q, want unknown", got)
	}
}
