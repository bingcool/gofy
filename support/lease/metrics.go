package lease

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Instrument names follow the OpenTelemetry dot convention; the Prometheus
// exporter turns dots into underscores and suffixes counters with _total.
const (
	metricAcquireFailures = "gofy.lease.acquire.failures"
	metricRenewFailures   = "gofy.lease.renew.failures"
	leaseMeterScope       = "github.com/bingcool/gofy/support/lease"
)

// leaseMetrics caches instruments for one Run call. Name is optional: without
// it only the aggregate series is emitted, keyed by outcome where applicable.
// leaseMetrics 为每次 Run 缓存 instrument。Name 可选：不配置时只发聚合序列，
// 在需要区分的地方用 outcome 维度。
type leaseMetrics struct {
	enabled bool
	name    string

	acquireFailures metric.Int64Counter
	renewFailures   metric.Int64Counter
}

func newLeaseMetrics(provider metric.MeterProvider, enabled bool, name string) (*leaseMetrics, error) {
	m := &leaseMetrics{name: name}
	// Metrics=true with a nil provider can only be a wiring mistake — no valid
	// configuration looks like this — so reject it instead of silently
	// running without metrics.
	// Metrics=true 且 provider 为 nil 只可能是接线错误——没有任何合法配置长这样
	// ——因此直接拒绝，而不是静默地跑成无指标。
	if enabled && provider == nil {
		return nil, errors.New("lease: Metrics is true but MeterProvider is nil; set both fields together")
	}
	if !enabled {
		return m, nil
	}
	meter := provider.Meter(leaseMeterScope)
	var err error
	if m.acquireFailures, err = meter.Int64Counter(metricAcquireFailures,
		metric.WithDescription("Lease acquisitions that failed; ErrHeld standby is excluded.")); err != nil {
		return nil, fmt.Errorf("lease: create %s: %w", metricAcquireFailures, err)
	}
	if m.renewFailures, err = meter.Int64Counter(metricRenewFailures,
		metric.WithDescription("Renewal failures by outcome (error or lost).")); err != nil {
		return nil, fmt.Errorf("lease: create %s: %w", metricRenewFailures, err)
	}
	m.enabled = true
	return m, nil
}

func (m *leaseMetrics) attrs(outcome string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 2)
	if m.name != "" {
		attrs = append(attrs, attribute.String("lease", m.name))
	}
	if outcome != "" {
		attrs = append(attrs, attribute.String("outcome", outcome))
	}
	return attrs
}

func (m *leaseMetrics) recordAcquireFailure() {
	if !m.enabled {
		return
	}
	m.acquireFailures.Add(context.Background(), 1, metric.WithAttributes(m.attrs("")...))
}

// recordRenewFailure distinguishes a transient error from a proven loss: an
// operator needs to know whether the holder was asked to stop writing.
// recordRenewFailure 区分暂时错误与确定丢失：运维需要知道持有者是否已被要求停止写入。
func (m *leaseMetrics) recordRenewFailure(lost bool) {
	if !m.enabled {
		return
	}
	outcome := "error"
	if lost {
		outcome = "lost"
	}
	m.renewFailures.Add(context.Background(), 1, metric.WithAttributes(m.attrs(outcome)...))
}
