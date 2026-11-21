package claim

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
const (
	metricClaimed        = "gofy.claim.claimed"
	metricOutcomes       = "gofy.claim.outcomes"
	metricHandlerSeconds = "gofy.claim.handler.duration"
	claimMeterScope      = "github.com/bingcool/gofy/support/claim"
)

// claimMetrics caches one set of instruments per Runner. Job is fixed at
// construction, so label values stay inside the configured vocabulary.
// claimMetrics 为每个 Runner 缓存一套 instrument。Job 在构造期固定，
// 标签值因此始终落在配置词汇表内。
type claimMetrics struct {
	enabled bool
	job     string

	claimed  metric.Int64Counter
	outcomes metric.Int64Counter
	duration metric.Float64Histogram
}

func newClaimMetrics(provider metric.MeterProvider, enabled bool, job string) (*claimMetrics, error) {
	m := &claimMetrics{job: job}
	// Metrics=true with a nil provider can only be a wiring mistake — no valid
	// configuration looks like this — so reject it instead of silently
	// running without metrics.
	// Metrics=true 且 provider 为 nil 只可能是接线错误——没有任何合法配置长这样
	// ——因此直接拒绝，而不是静默地跑成无指标。
	if enabled && provider == nil {
		return nil, errors.New("claim: Metrics is true but MeterProvider is nil; set both fields together")
	}
	if !enabled {
		return m, nil
	}
	meter := provider.Meter(claimMeterScope)
	var err error
	if m.claimed, err = meter.Int64Counter(metricClaimed,
		metric.WithDescription("Rows claimed per poll round.")); err != nil {
		return nil, fmt.Errorf("claim: create %s: %w", metricClaimed, err)
	}
	if m.outcomes, err = meter.Int64Counter(metricOutcomes,
		metric.WithDescription("Claimed rows by settled outcome.")); err != nil {
		return nil, fmt.Errorf("claim: create %s: %w", metricOutcomes, err)
	}
	if m.duration, err = meter.Float64Histogram(metricHandlerSeconds,
		metric.WithUnit("s"),
		metric.WithDescription("Duration of the business handler, excluding bookkeeping writes.")); err != nil {
		return nil, fmt.Errorf("claim: create %s: %w", metricHandlerSeconds, err)
	}
	m.enabled = true
	return m, nil
}

func (m *claimMetrics) recordClaimed(n int) {
	if !m.enabled {
		return
	}
	m.claimed.Add(context.Background(), int64(n), metric.WithAttributes(attribute.String("job", m.job)))
}

// recordOutcome is called once per claimed row, at the same point its hook
// fires. Bookkeeping-write errors have no settled outcome and are not counted.
// recordOutcome 每条认领行只调用一次，位置与其 hook 触发点一致。
// 收尾写库失败没有确定的结果，因此不计数。
func (m *claimMetrics) recordOutcome(outcome string, elapsed time.Duration) {
	if !m.enabled {
		return
	}
	attrs := metric.WithAttributes(
		attribute.String("job", m.job),
		attribute.String("outcome", outcome),
	)
	m.outcomes.Add(context.Background(), 1, attrs)
	if outcome != "lease_lost" {
		m.duration.Record(context.Background(), elapsed.Seconds(), attrs)
	}
}
