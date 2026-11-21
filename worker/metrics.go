package worker

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Instrument names follow the OpenTelemetry dot convention; the Prometheus
// exporter turns the dots into underscores and appends _total to counters.
// Instrument 名沿用 OpenTelemetry 的点号约定；Prometheus exporter 会把点转成
// 下划线，并给 counter 补上 _total。
const (
	metricWorkerReady          = "gofy.worker.ready"
	metricInstancesReady       = "gofy.worker.instances.ready"
	metricInstancesTotal       = "gofy.worker.instances.total"
	metricRestarts             = "gofy.worker.restarts"
	metricRunDuration          = "gofy.worker.run.duration"
	metricTickDuration         = "gofy.worker.tick.duration"
	metricTickErrors           = "gofy.worker.tick.errors"
	metricAbandoned            = "gofy.worker.abandoned"
	metricInstrumentationScope = "github.com/bingcool/gofy/worker"
)

// workerMetrics caches every instrument once. With enabled == false the struct
// is deliberately empty: recording methods return before building attributes,
// so a process without metrics pays no allocations on its hot paths.
// workerMetrics 只创建一次 instrument。enabled 为 false 时结构体是空的：
// 记录方法在构造标签之前就返回，没有指标管道的热路径因此不产生额外分配。
type workerMetrics struct {
	enabled bool
	group   string
	names   []string

	ready          metric.Int64Gauge
	instancesReady metric.Int64Gauge
	instancesTotal metric.Int64Gauge
	restarts       metric.Int64Counter
	runDuration    metric.Float64Histogram
	tickDuration   metric.Float64Histogram
	tickErrors     metric.Int64Counter
	abandoned      metric.Int64Counter
}

func newWorkerMetrics(provider metric.MeterProvider, enabled bool, group string, names []string) (*workerMetrics, error) {
	m := &workerMetrics{group: group, names: names}
	if !enabled || provider == nil {
		return m, nil
	}
	meter := provider.Meter(metricInstrumentationScope)
	var err error
	if m.ready, err = meter.Int64Gauge(metricWorkerReady,
		metric.WithDescription("Whether every instance of a worker is ready.")); err != nil {
		return nil, fmt.Errorf("worker: create %s: %w", metricWorkerReady, err)
	}
	if m.instancesReady, err = meter.Int64Gauge(metricInstancesReady,
		metric.WithDescription("Number of ready instances of a worker.")); err != nil {
		return nil, fmt.Errorf("worker: create %s: %w", metricInstancesReady, err)
	}
	if m.instancesTotal, err = meter.Int64Gauge(metricInstancesTotal,
		metric.WithDescription("Number of configured instances of a worker.")); err != nil {
		return nil, fmt.Errorf("worker: create %s: %w", metricInstancesTotal, err)
	}
	if m.restarts, err = meter.Int64Counter(metricRestarts,
		metric.WithDescription("Number of worker instance restarts.")); err != nil {
		return nil, fmt.Errorf("worker: create %s: %w", metricRestarts, err)
	}
	if m.runDuration, err = meter.Float64Histogram(metricRunDuration,
		metric.WithUnit("s"),
		metric.WithDescription("Duration of one worker Run call.")); err != nil {
		return nil, fmt.Errorf("worker: create %s: %w", metricRunDuration, err)
	}
	if m.tickDuration, err = meter.Float64Histogram(metricTickDuration,
		metric.WithUnit("s"),
		metric.WithDescription("Duration of one polling tick.")); err != nil {
		return nil, fmt.Errorf("worker: create %s: %w", metricTickDuration, err)
	}
	if m.tickErrors, err = meter.Int64Counter(metricTickErrors,
		metric.WithDescription("Number of failed or panicking polling ticks.")); err != nil {
		return nil, fmt.Errorf("worker: create %s: %w", metricTickErrors, err)
	}
	if m.abandoned, err = meter.Int64Counter(metricAbandoned,
		metric.WithDescription("Number of groups that outlived the drain budget.")); err != nil {
		return nil, fmt.Errorf("worker: create %s: %w", metricAbandoned, err)
	}
	m.enabled = true
	return m, nil
}

func (m *workerMetrics) workerAttrs(name string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("group", m.group),
		attribute.String("name", name),
	}
}

// setReady records the group-level readiness gauge for every worker name.
// setReady 为每个 worker 名记录分组级的就绪 gauge。
func (m *workerMetrics) setReady(value int64) {
	if !m.enabled {
		return
	}
	for _, name := range m.names {
		m.ready.Record(context.Background(), value, metric.WithAttributes(m.workerAttrs(name)...))
	}
}

// setInstances records total and ready counts per worker. pending holds the
// reporting instances that have not signalled yet, so subtracting by name also
// covers workers that do not implement ReadyReporter (they are ready at once).
// setInstances 按 worker 名记录总数与就绪数。pending 是尚未上报的 worker 实例，
// 按名字相减也覆盖了没有实现 ReadyReporter 的 worker（它们一开始就就绪）。
func (m *workerMetrics) setInstances(totals map[string]int, pending []Identity) {
	if !m.enabled {
		return
	}
	for _, name := range m.names {
		waiting := 0
		for _, id := range pending {
			if id.Name == name {
				waiting++
			}
		}
		attrs := metric.WithAttributes(m.workerAttrs(name)...)
		m.instancesTotal.Record(context.Background(), int64(totals[name]), attrs)
		m.instancesReady.Record(context.Background(), int64(totals[name]-waiting), attrs)
	}
}

func (m *workerMetrics) recordRestart(id Identity) {
	if !m.enabled {
		return
	}
	m.restarts.Add(context.Background(), 1, metric.WithAttributes(m.workerAttrs(id.Name)...))
}

// recordRun records one w.Run call. A nil error means a clean stop or a normal
// cancellation; any error is "error", including a fatal failure.
// recordRun 记录一次 w.Run 调用。nil 表示干净停机或正常取消；任何错误都归为 error，
// 其中也包含致命失败。
func (m *workerMetrics) recordRun(id Identity, elapsed time.Duration, failed bool) {
	if !m.enabled {
		return
	}
	outcome := "ok"
	if failed {
		outcome = "error"
	}
	attrs := metric.WithAttributes(
		attribute.String("group", id.Group),
		attribute.String("name", id.Name),
		attribute.String("outcome", outcome),
	)
	m.runDuration.Record(context.Background(), elapsed.Seconds(), attrs)
}

func (m *workerMetrics) recordAbandoned() {
	if !m.enabled {
		return
	}
	m.abandoned.Add(context.Background(), 1, metric.WithAttributes(attribute.String("group", m.group)))
}

// recordTick is called by runTick. The metrics pointer travels in the context
// so a Ticker nested inside a worker reports to the same provider as its runner.
// recordTick 由 runTick 调用。metrics 指针放在 context 里，worker 内部的 Ticker
// 因此与它的 runner 共用同一个 provider。
func (m *workerMetrics) recordTick(ctx context.Context, elapsed time.Duration, failed bool) {
	if !m.enabled {
		return
	}
	group, name := "unknown", "unknown"
	if id, ok := IdentityFrom(ctx); ok {
		group, name = id.Group, id.Name
	}
	attrs := []attribute.KeyValue{
		attribute.String("group", group),
		attribute.String("name", name),
	}
	m.tickDuration.Record(context.Background(), elapsed.Seconds(), metric.WithAttributes(attrs...))
	if failed {
		m.tickErrors.Add(context.Background(), 1, metric.WithAttributes(attrs...))
	}
}

type metricsKey struct{}

func withMetrics(ctx context.Context, m *workerMetrics) context.Context {
	if m == nil {
		return ctx
	}
	return context.WithValue(ctx, metricsKey{}, m)
}

func metricsFrom(ctx context.Context) *workerMetrics {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(metricsKey{}).(*workerMetrics)
	return m
}
