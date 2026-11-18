package otel

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.uber.org/zap"
)

// MetricsConfig describes the Prometheus metrics pipeline. Metrics are pulled,
// not pushed: there is no endpoint to configure, only the path the handler is
// mounted on.
// MetricsConfig 描述 Prometheus 指标管道。指标是拉取而非推送：没有 endpoint 需要
// 配置，只有 handler 挂载的路径。
type MetricsConfig struct {
	// Enabled turns the metrics pipeline on. When false no provider, registry or
	// handler is built and GinMiddleware returns nil.
	// Enabled 打开指标管道。为 false 时不构建 provider、registry 与 handler，
	// GinMiddleware 返回 nil。
	Enabled bool `yaml:"enabled"`

	// Path is where Handler is expected to be mounted. It defaults to /metrics
	// and is only a label: the handler itself is path-agnostic, the diagnostics
	// server reads it to register the route.
	// Path 是 Handler 预计挂载的路径，默认 /metrics。它只是一个标签：handler 本身
	// 与路径无关，诊断服务器读它来注册路由。
	Path string `yaml:"path" default:"/metrics"`
}

// DefaultMetricsPath is the conventional scrape path.
// DefaultMetricsPath 是约定的抓取路径。
const DefaultMetricsPath = "/metrics"

// durationBuckets are the histogram boundaries for HTTP request durations, in
// seconds. They start at 5ms so a fast internal call is not lumped into the
// first bucket together with a timeout, and stop at 10s because anything slower
// is already an incident rather than a latency observation.
// The slice is private on purpose: an exported one can be edited by any caller,
// changing the boundaries of Metrics built later — or racing a build in flight.
// A histogram's buckets are a dashboard contract, so they stay fixed here.
// durationBuckets 是 HTTP 请求耗时直方图的分桶边界（秒）。起点 5ms，避免把快速的内网
// 调用和超时一起塞进第一个桶；终点 10s，因为再慢已经是故障而不是延迟观测。
// 刻意设为私有：导出的切片可被任意调用方修改，既会改变之后创建的 Metrics 的边界，
// 也会与正在进行的构建竞争。直方图分桶是看板契约，因此固定在这里。
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Validate checks the configuration without building anything.
// Validate 只校验配置，不构建任何对象。
func (c MetricsConfig) Validate() error {
	path := strings.TrimSpace(c.Path)
	if path == "" {
		return nil
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("otel: metrics path %q must start with /", c.Path)
	}
	if strings.ContainsAny(path, " \t") {
		return fmt.Errorf("otel: metrics path %q must not contain spaces", c.Path)
	}
	return nil
}

func (c MetricsConfig) withDefaults() MetricsConfig {
	out := c
	out.Path = strings.TrimSpace(out.Path)
	if out.Path == "" {
		out.Path = DefaultMetricsPath
	}
	return out
}

// Metrics owns a meter provider, its Prometheus registry and the handler that
// exposes it. A nil *Metrics is valid and behaves like a disabled one, so
// callers can use it unconditionally.
// Metrics 持有 meter provider、它的 Prometheus registry 以及暴露用的 handler。
// nil 的 *Metrics 合法，行为等同于禁用状态，调用方可以无条件调用。
type Metrics struct {
	cfg      MetricsConfig
	logger   *zap.Logger
	enabled  bool
	provider *sdkmetric.MeterProvider
	handler  http.Handler

	duration metric.Float64Histogram
	active   metric.Int64UpDownCounter

	shutdownOnce sync.Once
	shutdownErr  error
}

// NewMetrics validates cfg and builds the metrics pipeline. The registry is
// private to this instance: the Prometheus default registerer is process-wide,
// and a second App (or a test that builds a second App) would get a duplicate
// registration error instead of an isolated pipeline.
// NewMetrics 校验 cfg 并构建指标管道。registry 由本实例私有：Prometheus 的默认
// 注册器是进程级的，第二个 App（或构建第二个 App 的测试）会拿到重复注册错误，
// 而不是一套彼此隔离的管道。
//
// ServiceName, ServiceVersion and Environment come from the same options as New,
// so metrics and traces describe the same service in the same backend.
// ServiceName、ServiceVersion 与 Environment 与 New 使用相同的选项，因此指标与
// Trace 在后端描述的是同一个服务。
func NewMetrics(cfg MetricsConfig, opts ...Option) (*Metrics, error) {
	var po providerOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&po)
		}
	}
	if po.logger == nil {
		po.logger = zap.NewNop()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	m := &Metrics{cfg: cfg, logger: po.logger}
	if !cfg.Enabled {
		return m, nil
	}

	// Reuse the tracing resource builder so both pipelines carry identical
	// service.name, host and pid attributes; resource.Default also picks up
	// OTEL_RESOURCE_ATTRIBUTES.
	// 复用 tracing 的资源构建器，两条管道因此带有相同的 service.name、host 与 pid；
	// resource.Default 还会读取 OTEL_RESOURCE_ATTRIBUTES。
	res, err := buildResource(Config{
		ServiceName:    po.serviceName,
		ServiceVersion: po.serviceVersion,
		Environment:    po.environment,
	})
	if err != nil {
		return nil, err
	}

	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		return nil, fmt.Errorf("otel: build prometheus exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(exporter),
		// Without this view the SDK uses its own defaults, whose upper bound is
		// far above any useful HTTP latency. The boundaries are explicit so a
		// dashboard does not silently change when the SDK default changes.
		// 没有这个 view 时 SDK 用自己的默认桶，上界远高于有意义的 HTTP 延迟。显式
		// 给出边界，看板不会因为 SDK 默认值变化而悄悄改变。
		sdkmetric.WithView(sdkmetric.NewView(
			sdkmetric.Instrument{Name: "http.server.request.duration"},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
				Boundaries: append([]float64(nil), durationBuckets...),
			}},
		)),
	)

	meter := mp.Meter("github.com/bingcool/gofy")
	if err := m.buildInstruments(meter); err != nil {
		_ = mp.Shutdown(context.Background())
		return nil, err
	}
	if err := registerRuntimeMetrics(meter); err != nil {
		_ = mp.Shutdown(context.Background())
		return nil, err
	}

	m.provider = mp
	m.enabled = true
	m.handler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		// One broken collector must not turn the whole scrape into a 500: a
		// partial answer still tells an operator more than none.
		// 单个采集器出错不该让整次抓取变成 500：部分答案仍然胜过没有答案。
		ErrorHandling:     promhttp.ContinueOnError,
		EnableOpenMetrics: true,
	})
	return m, nil
}

// Enabled reports whether a real meter provider is installed.
// Enabled 报告是否已安装真实的 meter provider。
func (m *Metrics) Enabled() bool { return m != nil && m.enabled }

// Handler returns the scrape handler, or nil when metrics are disabled.
// Handler 返回抓取用的 handler；指标禁用时返回 nil。
func (m *Metrics) Handler() http.Handler {
	if !m.Enabled() {
		return nil
	}
	return m.handler
}

// Path returns the configured scrape path.
// Path 返回配置的抓取路径。
func (m *Metrics) Path() string {
	if m == nil {
		return DefaultMetricsPath
	}
	if m.cfg.Path == "" {
		return DefaultMetricsPath
	}
	return m.cfg.Path
}

// Shutdown releases the meter provider. It is safe to call more than once and on
// a disabled Metrics.
// Shutdown 释放 meter provider。重复调用与对禁用的 Metrics 调用都安全。
func (m *Metrics) Shutdown(ctx context.Context) error {
	if !m.Enabled() {
		return nil
	}
	m.shutdownOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		m.shutdownErr = m.provider.Shutdown(ctx)
	})
	return m.shutdownErr
}

func (m *Metrics) buildInstruments(meter metric.Meter) error {
	var err error
	m.duration, err = meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of inbound HTTP requests."),
	)
	if err != nil {
		return fmt.Errorf("otel: create request duration histogram: %w", err)
	}
	m.active, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithUnit("{request}"),
		metric.WithDescription("Number of inbound HTTP requests currently being served."),
	)
	if err != nil {
		return fmt.Errorf("otel: create active requests counter: %w", err)
	}
	return nil
}

// GinMiddleware records request count, duration and in-flight gauge. It is nil
// when metrics are disabled, so callers can skip installing it.
//
// The route label is the registered pattern from gin (FullPath), never the raw
// URL: labelling by actual path turns every id in a REST API into a new time
// series and exhausts the backend. Unmatched requests collapse into one series.
// filter receives each request and returns true when it should be recorded; nil
// records everything, which would let probes dominate the numbers.
// GinMiddleware 记录请求数、耗时与在途数量。指标禁用时返回 nil，调用方可直接跳过安装。
//
// route 标签取自 gin 注册的模式（FullPath），绝不用原始 URL：按实际路径打标签会让
// REST API 里的每个 id 变成一条新时间线，把后端撑爆。未匹配任何路由的请求收敛为一条。
// filter 对每个请求求值，返回 true 表示需要记录；nil 表示全部记录——探针会因此主导
// 指标数值。
func (m *Metrics) GinMiddleware(filter func(*http.Request) bool) gin.HandlerFunc {
	if !m.Enabled() {
		return nil
	}
	return func(c *gin.Context) {
		if filter != nil && !filter(c.Request) {
			c.Next()
			return
		}

		// The in-flight gauge is updated around the handler, and the duration is
		// recorded in a defer so a panic that Recovery turns into a 500 is still
		// observed. Recording uses a background context: by the time the defer
		// runs, the request context may already be cancelled, and the SDK must
		// not skip the measurement because of it.
		// 在途计数围绕 handler 增减；耗时放在 defer 里记录，这样 Recovery 把 panic
		// 转成 500 之后仍然留下观测值。记录使用 background context：defer 执行时请求
		// context 可能已取消，SDK 不能因此丢掉这次测量。
		ctx := c.Request.Context()
		start := time.Now()
		m.active.Add(ctx, 1)

		defer func() {
			m.active.Add(context.Background(), -1)
			m.duration.Record(context.Background(), time.Since(start).Seconds(),
				metric.WithAttributes(
					semconv.HTTPRequestMethodKey.String(normalizeMethod(c.Request.Method)),
					attribute.String("http.route", routeLabel(c)),
					attribute.Int("http.response.status_code", c.Writer.Status()),
				),
			)
		}()

		c.Next()
	}
}

// routeLabel returns the registered pattern, or a single bucket for requests
// that matched no route.
// routeLabel 返回注册的路由模式；没有匹配任何路由时返回一个固定桶。
func routeLabel(c *gin.Context) string {
	if path := c.FullPath(); path != "" {
		return path
	}
	return "unmatched"
}

// knownMethods limits the method label to the standard set. net/http accepts an
// arbitrary token as the method, so without this a client could create one time
// series per invented verb.
// knownMethods 把 method 标签限制在标准集合内。net/http 接受任意 token 作为方法，
// 没有这一步，客户端每发明一个动词就能造出一条新的时间线。
func normalizeMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect,
		http.MethodOptions, http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

// registerRuntimeMetrics installs the Go runtime gauges. One callback reads
// runtime.MemStats once and feeds every memory instrument, because ReadMemStats
// stops the world; calling it per instrument would multiply that cost by the
// number of gauges for no benefit.
//
// The names follow the OpenTelemetry runtime semantic conventions, so a backend
// that already understands process.runtime.go.* renders them without a mapping.
// registerRuntimeMetrics 安装 Go 运行时 gauge。一个回调只读一次 runtime.MemStats
// 并喂给所有内存 instrument，因为 ReadMemStats 会 STW；每个 instrument 各读一次
// 会让这份开销按 gauge 数量翻倍而毫无收益。
//
// 指标名遵循 OpenTelemetry 运行时语义约定，已经认识 process.runtime.go.* 的后端
// 无需映射即可渲染。
func registerRuntimeMetrics(meter metric.Meter) error {
	goroutines, err := meter.Int64ObservableGauge(
		"process.runtime.go.goroutines",
		metric.WithUnit("{goroutine}"),
		metric.WithDescription("Number of goroutines that currently exist."),
	)
	if err != nil {
		return fmt.Errorf("otel: create goroutine gauge: %w", err)
	}

	type memoryGauge struct {
		name        string
		unit        string
		description string
	}
	gauges := make(map[string]metric.Int64ObservableGauge, 6)
	for _, spec := range []memoryGauge{
		{"process.runtime.go.mem.heap_alloc", "By", "Bytes of allocated heap objects."},
		{"process.runtime.go.mem.heap_inuse", "By", "Bytes in in-use spans."},
		{"process.runtime.go.mem.heap_objects", "{object}", "Number of allocated heap objects."},
		{"process.runtime.go.mem.stack_inuse", "By", "Bytes in stack spans."},
		{"process.runtime.go.gc.count", "{gc}", "Number of completed GC cycles."},
		{"process.runtime.go.gc.pause_total", "ns", "Cumulative GC pause time."},
	} {
		g, err := meter.Int64ObservableGauge(
			spec.name,
			metric.WithUnit(spec.unit),
			metric.WithDescription(spec.description),
		)
		if err != nil {
			return fmt.Errorf("otel: create runtime gauge %s: %w", spec.name, err)
		}
		gauges[spec.name] = g
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)

		o.ObserveInt64(goroutines, int64(runtime.NumGoroutine()))
		o.ObserveInt64(gauges["process.runtime.go.mem.heap_alloc"], int64(ms.HeapAlloc))
		o.ObserveInt64(gauges["process.runtime.go.mem.heap_inuse"], int64(ms.HeapInuse))
		o.ObserveInt64(gauges["process.runtime.go.mem.heap_objects"], int64(ms.HeapObjects))
		o.ObserveInt64(gauges["process.runtime.go.mem.stack_inuse"], int64(ms.StackInuse))
		o.ObserveInt64(gauges["process.runtime.go.gc.count"], int64(ms.NumGC))
		o.ObserveInt64(gauges["process.runtime.go.gc.pause_total"], int64(ms.PauseTotalNs))
		return nil
	}, goroutines,
		gauges["process.runtime.go.mem.heap_alloc"],
		gauges["process.runtime.go.mem.heap_inuse"],
		gauges["process.runtime.go.mem.heap_objects"],
		gauges["process.runtime.go.mem.stack_inuse"],
		gauges["process.runtime.go.gc.count"],
		gauges["process.runtime.go.gc.pause_total"],
	)
	if err != nil {
		return fmt.Errorf("otel: register runtime callback: %w", err)
	}
	return nil
}
