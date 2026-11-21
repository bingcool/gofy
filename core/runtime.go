// Package core holds the runtime dependencies shared by the gofy root
// package, scripts and custom commands. It is a leaf package: it must not
// import any other gofy package.
// 包 core 持有 gofy 根包、脚本与自定义命令共享的运行时依赖。它是叶子包：不得
// 导入任何其他 gofy 包，以免形成循环依赖。
package core

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// VersionInfo describes the build an application is running.
// VersionInfo 描述应用当前运行的构建信息。
type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
}

// Runtime is the set of dependencies built by the framework during startup.
// Business code reads it; it is not a dependency-injection container.
// Runtime 是框架在启动期构建的依赖集合。业务代码只读它，它不是依赖注入容器。
type Runtime struct {
	Name      string
	Env       string
	Logger    *zap.Logger
	DB        *gorm.DB
	Version   VersionInfo
	StartedAt time.Time

	// TracerProvider lets business code create spans; it is a no-op provider when
	// tracing is off.
	// TracerProvider 供业务代码创建 span；未启用 tracing 时是 no-op provider。
	TracerProvider trace.TracerProvider
	// Propagator is the W3C TraceContext + Baggage propagator used inbound and
	// outbound.
	// Propagator 是入站和出站使用的 W3C TraceContext + Baggage 传播器。
	Propagator propagation.TextMapPropagator
	// Tracing reports that the App enabled OpenTelemetry export. Business code uses it
	// to decide whether to install optional instrumentation such as GORM or Redis,
	// avoiding the extra cost of a no-op tracer.
	// Tracing 表示 App 已启用 OpenTelemetry 导出。业务据此决定是否安装
	// GORM、Redis 等可选埋点，避免 no-op tracer 带来的额外开销。
	Tracing bool

	// MeterProvider lets business code and worker runtimes create instruments.
	// It is a no-op provider when metrics are off; Metrics reports whether the
	// pipeline is real, so hot paths can skip building attributes entirely.
	// MeterProvider 供业务代码与 worker 运行时创建 instrument。指标关闭时它是
	// no-op provider；Metrics 表示管道是否真实存在，热路径因此可以连属性都不构造。
	MeterProvider metric.MeterProvider
	// Metrics reports whether the App built a Prometheus pipeline. Metrics are
	// independent of tracing: an app can scrape metrics without a collector.
	// Metrics 表示 App 是否构建了 Prometheus 管道。指标与追踪相互独立：
	// 没有 collector 的应用仍然可以抓取指标。
	Metrics bool

	ctx  context.Context
	fail func(error)
}

// RuntimeConfig contains the fields used to construct a Runtime.
// Applications normally do not call this directly.
// RuntimeConfig 包含构造 Runtime 所需的字段，应用通常不直接调用它。
type RuntimeConfig struct {
	Name      string
	Env       string
	Logger    *zap.Logger
	DB        *gorm.DB
	Version   VersionInfo
	StartedAt time.Time
	Ctx       context.Context
	Fail      func(error)

	TracerProvider trace.TracerProvider
	Propagator     propagation.TextMapPropagator
	Tracing        bool
	MeterProvider  metric.MeterProvider
	Metrics        bool
}

// NewRuntime builds a Runtime from cfg, filling safe defaults for nil fields.
// NewRuntime 按 cfg 构造 Runtime，并为 nil 字段填入安全的默认值。
func NewRuntime(cfg RuntimeConfig) *Runtime {
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
	if cfg.Fail == nil {
		cfg.Fail = func(error) {}
	}
	if cfg.StartedAt.IsZero() {
		cfg.StartedAt = time.Now()
	}
	if cfg.TracerProvider == nil {
		cfg.TracerProvider = noop.NewTracerProvider()
	}
	if cfg.Propagator == nil {
		cfg.Propagator = propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		)
	}
	if cfg.MeterProvider == nil {
		cfg.MeterProvider = metricnoop.NewMeterProvider()
	}
	return &Runtime{
		Name:           cfg.Name,
		Env:            cfg.Env,
		Logger:         cfg.Logger,
		DB:             cfg.DB,
		Version:        cfg.Version,
		StartedAt:      cfg.StartedAt,
		TracerProvider: cfg.TracerProvider,
		Propagator:     cfg.Propagator,
		Tracing:        cfg.Tracing,
		MeterProvider:  cfg.MeterProvider,
		Metrics:        cfg.Metrics,
		ctx:            cfg.Ctx,
		fail:           cfg.Fail,
	}
}

// Context is cancelled when the application starts shutting down.
// Background goroutines should watch it instead of registering signals.
// Context 在应用开始关闭时被取消。后台 goroutine 应监听它，而不是自行注册信号。
func (r *Runtime) Context() context.Context {
	if r == nil || r.ctx == nil {
		return context.Background()
	}
	return r.ctx
}

// Fail reports a fatal error and triggers application shutdown.
// The first call wins; later calls are ignored. Fail never panics.
// Fail 上报致命错误并触发应用关闭。先到者生效，后续调用被忽略；Fail 从不 panic。
func (r *Runtime) Fail(err error) {
	if r == nil || r.fail == nil || err == nil {
		return
	}
	r.fail(err)
}
