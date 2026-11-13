package otel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
	"google.golang.org/grpc/credentials"
)

const defaultServiceName = "unknown_service"

// Provider owns a tracer provider and its exporter. A nil Provider is valid
// and behaves like a disabled one, so callers can use it unconditionally.
type Provider struct {
	cfg          Config
	logger       *zap.Logger
	serviceName  string
	enabled      bool
	tp           trace.TracerProvider
	sdk          *sdktrace.TracerProvider
	propagator   propagation.TextMapPropagator
	shutdownOnce sync.Once
	shutdownErr  error
}

type providerOptions struct {
	logger         *zap.Logger
	serviceName    string
	serviceVersion string
	environment    string
	exporter       sdktrace.SpanExporter
}

// Option customizes New. Options override the matching Config fields only when
// the option value is non-empty.
type Option func(*providerOptions)

// WithLogger sets the logger used for exporter errors and shutdown failures.
func WithLogger(logger *zap.Logger) Option {
	return func(o *providerOptions) {
		if logger != nil {
			o.logger = logger
		}
	}
}

// WithServiceName sets service.name when the Config does not define it.
func WithServiceName(name string) Option {
	return func(o *providerOptions) {
		if v := strings.TrimSpace(name); v != "" {
			o.serviceName = v
		}
	}
}

// WithServiceVersion sets service.version when the Config does not define it.
func WithServiceVersion(version string) Option {
	return func(o *providerOptions) {
		if v := strings.TrimSpace(version); v != "" {
			o.serviceVersion = v
		}
	}
}

// WithEnvironment sets deployment.environment.name when the Config does not
// define it.
func WithEnvironment(env string) Option {
	return func(o *providerOptions) {
		if v := strings.TrimSpace(env); v != "" {
			o.environment = v
		}
	}
}

// WithSpanExporter replaces the OTLP exporter. It is useful for tests and for
// applications that ship spans somewhere other than an OTLP collector.
func WithSpanExporter(exporter sdktrace.SpanExporter) Option {
	return func(o *providerOptions) {
		if exporter != nil {
			o.exporter = exporter
		}
	}
}

// New validates cfg and builds the tracing pipeline. It does not connect to
// the collector; export failures are retried in the background and reported
// through the logger. A disabled Config returns a disabled Provider without
// creating an exporter.
//
// 进程级全局设置（ErrorHandler、TracerProvider、TextMapPropagator）只在
// Config.Global 为 true 时安装；Global=false 时创建和关闭 Provider 都不会
// 改动它们。同一进程如果要运行多个互不干扰的 App，必须保持 Global=false。
func New(ctx context.Context, cfg Config, opts ...Option) (*Provider, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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
	if cfg.ServiceName == "" {
		cfg.ServiceName = po.serviceName
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = defaultServiceName
	}
	if cfg.ServiceVersion == "" {
		cfg.ServiceVersion = po.serviceVersion
	}
	if cfg.Environment == "" {
		cfg.Environment = po.environment
	}

	p := &Provider{
		cfg:         cfg,
		logger:      po.logger,
		serviceName: cfg.ServiceName,
	}
	if !cfg.Enabled {
		p.tp = noop.NewTracerProvider()
		p.propagator = defaultPropagator()
		return p, nil
	}

	exporter := po.exporter
	if exporter == nil {
		var err error
		exporter, err = newExporter(cfg)
		if err != nil {
			return nil, err
		}
	}
	res, err := buildResource(cfg)
	if err != nil {
		return nil, err
	}

	// 导出失败由本 Provider 自己记录，不依赖进程级 ErrorHandler：Global=false
	// 时也能看到失败，而且不会因为多个 App 争抢全局处理器而互相覆盖。
	report := &errorHandler{logger: po.logger}
	exporter = &loggingExporter{next: exporter, report: report}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(samplerFor(cfg)),
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(cfg.Batch.Timeout),
			sdktrace.WithMaxQueueSize(cfg.Batch.MaxQueueSize),
			sdktrace.WithMaxExportBatchSize(cfg.Batch.MaxExportBatchSize),
			sdktrace.WithExportTimeout(cfg.Batch.ExportTimeout),
		),
	)
	propagator := defaultPropagator()
	if cfg.Global {
		// 全局模式下四项设置一起安装；ErrorHandler 也只在此时替换。与导出包装
		// 共用同一个 report，重复错误在去重窗口内不会打印两次。
		otelapi.SetErrorHandler(report)
		otelapi.SetTracerProvider(tp)
		otelapi.SetTextMapPropagator(propagator)
	}
	p.tp = tp
	p.sdk = tp
	p.propagator = propagator
	p.enabled = true
	return p, nil
}

// Enabled reports whether a real exporter is installed.
func (p *Provider) Enabled() bool { return p != nil && p.enabled }

// ServiceName returns service.name used for spans and the Gin middleware.
func (p *Provider) ServiceName() string {
	if p == nil {
		return ""
	}
	return p.serviceName
}

// TracerProvider returns the tracer provider. It is never nil; a disabled
// Provider returns a no-op provider.
func (p *Provider) TracerProvider() trace.TracerProvider {
	if p == nil || p.tp == nil {
		return noop.NewTracerProvider()
	}
	return p.tp
}

// Propagator returns the W3C TraceContext and Baggage propagator. It is never
// nil.
func (p *Provider) Propagator() propagation.TextMapPropagator {
	if p == nil || p.propagator == nil {
		return defaultPropagator()
	}
	return p.propagator
}

// Tracer returns a tracer from the underlying provider.
func (p *Provider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return p.TracerProvider().Tracer(name, opts...)
}

// Config returns the effective configuration with defaults applied.
func (p *Provider) Config() Config {
	if p == nil {
		return Config{}
	}
	return p.cfg
}

// ForceFlush exports every buffered span before the deadline. It is a no-op
// when tracing is disabled.
func (p *Provider) ForceFlush(ctx context.Context) error {
	if p == nil || p.sdk == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return p.sdk.ForceFlush(ctx)
}

// Shutdown flushes pending spans and releases the exporter. It is idempotent
// and safe to call on a nil or disabled Provider. The process-wide providers
// installed by Config.Global are left untouched, because other components may
// still hold references to them.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.sdk == nil {
		return nil
	}
	p.shutdownOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		timeout := p.cfg.ShutdownTimeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > timeout {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		p.shutdownErr = p.sdk.Shutdown(ctx)
		if p.shutdownErr != nil {
			p.logger.Warn("opentelemetry shutdown failed", zap.Error(p.shutdownErr))
		}
	})
	return p.shutdownErr
}

func defaultPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func samplerFor(cfg Config) sdktrace.Sampler {
	ratio := cfg.SampleRatio
	switch cfg.Sampler {
	case SamplerAlwaysOn:
		return sdktrace.AlwaysSample()
	case SamplerAlwaysOff:
		return sdktrace.NeverSample()
	case SamplerTraceIDRatio:
		return sdktrace.TraceIDRatioBased(ratio)
	case SamplerParentBasedAlwaysOn:
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case SamplerParentBasedAlwaysOff:
		return sdktrace.ParentBased(sdktrace.NeverSample())
	default:
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	}
}

func buildResource(cfg Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(cfg.ServiceName),
	}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.ServiceVersion))
	}
	if cfg.Environment != "" {
		attrs = append(attrs, semconv.DeploymentEnvironmentNameKey.String(cfg.Environment))
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		attrs = append(attrs, semconv.HostName(host))
	}
	attrs = append(attrs, semconv.ProcessPID(os.Getpid()))
	for _, key := range sortedKeys(cfg.Attributes) {
		attrs = append(attrs, attribute.String(key, cfg.Attributes[key]))
	}
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL, attrs...),
	)
	if err != nil {
		return nil, fmt.Errorf("otel: build resource: %w", err)
	}
	return res, nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func newExporter(cfg Config) (sdktrace.SpanExporter, error) {
	if cfg.Protocol == ProtocolHTTP {
		return newHTTPExporter(cfg)
	}
	return newGRPCExporter(cfg)
}

func newGRPCExporter(cfg Config) (sdktrace.SpanExporter, error) {
	opts := []otlptracegrpc.Option{otlptracegrpc.WithTimeout(cfg.Timeout)}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(cfg.Headers))
	}
	if cfg.Compression == CompressionGzip {
		opts = append(opts, otlptracegrpc.WithCompressor(CompressionGzip))
	}
	if hasScheme(cfg.Endpoint) {
		opts = append(opts, otlptracegrpc.WithEndpointURL(cfg.Endpoint))
	} else {
		opts = append(opts, otlptracegrpc.WithEndpoint(cfg.Endpoint))
	}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	} else {
		tlsCfg, err := cfg.tlsConfig()
		if err != nil {
			return nil, err
		}
		opts = append(opts, otlptracegrpc.WithTLSCredentials(credentials.NewTLS(tlsCfg)))
	}
	return otlptracegrpc.New(context.Background(), opts...)
}

func newHTTPExporter(cfg Config) (sdktrace.SpanExporter, error) {
	opts := []otlptracehttp.Option{otlptracehttp.WithTimeout(cfg.Timeout)}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
	}
	if cfg.Compression == CompressionGzip {
		opts = append(opts, otlptracehttp.WithCompression(otlptracehttp.GzipCompression))
	} else {
		opts = append(opts, otlptracehttp.WithCompression(otlptracehttp.NoCompression))
	}
	if hasScheme(cfg.Endpoint) {
		opts = append(opts, otlptracehttp.WithEndpointURL(cfg.Endpoint))
	} else {
		opts = append(opts, otlptracehttp.WithEndpoint(cfg.Endpoint))
	}
	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	} else {
		tlsCfg, err := cfg.tlsConfig()
		if err != nil {
			return nil, err
		}
		opts = append(opts, otlptracehttp.WithTLSClientConfig(tlsCfg))
	}
	return otlptracehttp.New(context.Background(), opts...)
}

func (c Config) tlsConfig() (*tls.Config, error) {
	out := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         strings.TrimSpace(c.TLS.ServerName),
		InsecureSkipVerify: c.TLS.InsecureSkipVerify,
	}
	if file := strings.TrimSpace(c.TLS.CAFile); file != "" {
		pem, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("otel: read ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("otel: ca_file contains no PEM certificates")
		}
		out.RootCAs = pool
	}
	if file := strings.TrimSpace(c.TLS.CertFile); file != "" {
		cert, err := tls.LoadX509KeyPair(file, strings.TrimSpace(c.TLS.KeyFile))
		if err != nil {
			return nil, fmt.Errorf("otel: load client certificate: %w", err)
		}
		out.Certificates = []tls.Certificate{cert}
	}
	return out, nil
}

func hasScheme(endpoint string) bool {
	return strings.Contains(endpoint, "://")
}

// loggingExporter 包装真实 exporter：导出失败先用 Provider 的 logger 记录，
// 再把错误原样交回 BatchSpanProcessor。Global=false 时也能看到导出问题，
// 不需要覆盖进程级 ErrorHandler；Global=true 时与全局处理器共用同一个
// errorHandler，去重窗口内不会打印两次。
type loggingExporter struct {
	next   sdktrace.SpanExporter
	report *errorHandler
}

func (e *loggingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := e.next.ExportSpans(ctx, spans)
	e.report.Handle(err)
	return err
}

func (e *loggingExporter) Shutdown(ctx context.Context) error {
	return e.next.Shutdown(ctx)
}

// errorHandler forwards OTel internal errors to zap and suppresses repeats for
// a minute, so an unreachable collector cannot flood the log.
type errorHandler struct {
	logger     *zap.Logger
	mu         sync.Mutex
	last       time.Time
	suppressed int
}

func (h *errorHandler) Handle(err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if !h.last.IsZero() && now.Sub(h.last) < time.Minute {
		h.suppressed++
		return
	}
	fields := []zap.Field{zap.Error(err)}
	if h.suppressed > 0 {
		fields = append(fields, zap.Int("suppressed", h.suppressed))
		h.suppressed = 0
	}
	h.last = now
	h.logger.Warn("opentelemetry error", fields...)
}
