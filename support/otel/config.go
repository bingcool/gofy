// Package otel provides production-oriented OpenTelemetry tracing for gofy:
// a validated configuration, an OTLP exporter with batch processing, W3C
// propagation and instrumentation helpers for Gin, net/http, GORM and Redis.
//
// The package installs no process-wide state unless Config.Global is enabled.
// With Global=false a Provider only owns its SDK tracer provider, exporter and
// propagator; export failures are logged through the Provider's logger. With
// Global=true it additionally replaces otel.SetErrorHandler,
// otel.SetTracerProvider and otel.SetTextMapPropagator. Only one App per
// process may use Global=true; competing Apps would overwrite each other's
// globals. Callers receive a *Provider and decide when to shut it down;
// Shutdown never touches the global providers.
// 包 otel 为 gofy 提供面向生产环境的 OpenTelemetry 追踪能力：经过校验的配置、
// 带批处理的 OTLP exporter、W3C 传播，以及 Gin、net/http、GORM、Redis 的埋点
// 辅助函数。
// 默认不安装任何进程级状态，只有 Config.Global 打开时才安装。Global=false 时
// Provider 只持有自己的 SDK tracer provider、exporter 与 propagator，导出失败
// 由 Provider 的 logger 记录；Global=true 时还会替换 otel.SetErrorHandler、
// otel.SetTracerProvider 与 otel.SetTextMapPropagator。
// 同一进程只允许一个 App 使用 Global=true，多个 App 会互相覆盖彼此的全局设置。
// 调用方拿到 *Provider 后自行决定何时关闭，Shutdown 不会改动全局 provider。
package otel

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Exporter protocols.
// Exporter 传输协议：gRPC 或 HTTP。
const (
	ProtocolGRPC = "grpc"
	ProtocolHTTP = "http"
)

// Compression modes.
// 导出压缩方式：gzip 或 none。
const (
	CompressionGzip = "gzip"
	CompressionNone = "none"
)

// Samplers accepted by Config.Sampler.
// Config.Sampler 可接受的采样器取值。
const (
	SamplerParentBasedTraceIDRatio = "parentbased_traceidratio"
	SamplerParentBasedAlwaysOn     = "parentbased_always_on"
	SamplerParentBasedAlwaysOff    = "parentbased_always_off"
	SamplerTraceIDRatio            = "traceidratio"
	SamplerAlwaysOn                = "always_on"
	SamplerAlwaysOff               = "always_off"
)

// Config describes the tracing pipeline. Zero values are replaced by the
// documented defaults, so Config{Enabled: true} is a complete local setup.
// Config 描述整条追踪链路。零值会被文档中给出的默认值替换，因此
// Config{Enabled: true} 就是一套可用的本地配置。
type Config struct {
	// Enabled turns tracing on. When false the framework installs no
	// middleware and builds no exporter.
	// Enabled 打开追踪开关。为 false 时框架不安装中间件，也不构建 exporter。
	Enabled bool `yaml:"enabled"`

	// Endpoint is the OTLP collector address. With a scheme (http://host:port)
	// it is used as a URL; without one it is treated as host:port. The default
	// is localhost:4317 for gRPC and localhost:4318 for HTTP.
	// Endpoint 是 OTLP collector 地址。带 scheme（http://host:port）时按 URL
	// 解析，不带时视为 host:port。默认值 gRPC 为 localhost:4317，
	// HTTP 为 localhost:4318。
	Endpoint string `yaml:"endpoint"`

	// Protocol selects the OTLP transport: grpc or http.
	// Protocol 选择 OTLP 传输方式：grpc 或 http。
	Protocol string `yaml:"protocol" default:"grpc"`

	// Insecure disables TLS, for collectors reached over a private network.
	// Insecure 关闭 TLS，适用于通过内网访问的 collector。
	Insecure bool `yaml:"insecure"`

	// Headers are sent with every export, for example an API token. Values are
	// never logged.
	// Headers 随每次导出发送，例如 API token。其取值永不写入日志。
	Headers map[string]string `yaml:"headers"`

	// Compression is gzip or none.
	// Compression 取 gzip 或 none。
	Compression string `yaml:"compression" default:"gzip"`

	// Timeout bounds one export attempt.
	// Timeout 限制单次导出尝试的耗时。
	Timeout time.Duration `yaml:"timeout" default:"10s"`

	// Sampler is the sampling strategy. The default keeps the decision of the
	// calling service and samples roots by SampleRatio.
	// Sampler 是采样策略。默认沿用调用方服务的采样决定，并按 SampleRatio
	// 对根 Trace 采样。
	Sampler string `yaml:"sampler" default:"parentbased_traceidratio"`

	// SampleRatio samples that fraction of root traces, between 0 and 1.
	// Zero means the default (1.0); use sampler: always_off to disable
	// sampling explicitly.
	// SampleRatio 指定根 Trace 的采样比例，取值 0 到 1。0 视为默认值 1.0；
	// 需要显式关闭采样请设 sampler: always_off。
	SampleRatio float64 `yaml:"sample_ratio" default:"1"`

	// ServiceName, ServiceVersion and Environment become the
	// service.name, service.version and deployment.environment.name resource
	// attributes. The framework fills empty values from the App name, build
	// version and environment.
	// ServiceName、ServiceVersion、Environment 依次成为 service.name、
	// service.version 与 deployment.environment.name 资源属性。框架会用
	// App 名称、构建版本和环境填充空值。
	ServiceName    string `yaml:"service_name"`
	ServiceVersion string `yaml:"service_version"`
	Environment    string `yaml:"environment"`

	// Attributes are extra resource attributes. Keys equal to the ones derived
	// above are appended, so the last value wins in most backends.
	// Attributes 是额外的资源属性。与上面推导出的键重复时会追加，多数后端
	// 以最后一个值为准。
	Attributes map[string]string `yaml:"attributes"`

	// Global installs the ErrorHandler, tracer provider and propagator as the
	// process-wide OTel defaults, so third-party libraries can use them without
	// being wired. Leave it false when the process hosts more than one App:
	// every Global App overwrites the previous one's globals. Export failures
	// are logged through the Provider logger in both modes.
	// Global 把 ErrorHandler、tracer provider 和 propagator 安装为进程级
	// OTel 默认值，第三方库无需接线即可使用。同一进程运行多个 App 时必须保持
	// false：每个 Global App 都会覆盖前一个的全局设置。两种模式下导出失败都
	// 通过 Provider 的 logger 记录。
	Global bool `yaml:"global"`

	// Batch controls the batch span processor.
	// Batch 控制 batch span processor。
	Batch BatchConfig `yaml:"batch"`

	// TLS configures the exporter connection when Insecure is false.
	// TLS 在 Insecure 为 false 时配置 exporter 连接。
	TLS TLSConfig `yaml:"tls"`

	// ShutdownTimeout bounds the final flush during shutdown.
	// ShutdownTimeout 限制关闭时最后一次 flush 的耗时。
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout" default:"5s"`
}

// BatchConfig controls the batch span processor. All fields map directly to
// sdktrace.BatchSpanProcessorOption.
// BatchConfig 控制 batch span processor，所有字段直接映射到
// sdktrace.BatchSpanProcessorOption。
type BatchConfig struct {
	Timeout            time.Duration `yaml:"timeout" default:"5s"`
	MaxQueueSize       int           `yaml:"max_queue_size" default:"2048"`
	MaxExportBatchSize int           `yaml:"max_export_batch_size" default:"512"`
	ExportTimeout      time.Duration `yaml:"export_timeout" default:"30s"`
}

// TLSConfig configures the TLS client used by the OTLP exporter.
// TLSConfig 配置 OTLP exporter 使用的 TLS 客户端。
type TLSConfig struct {
	// CAFile replaces the system roots with the given PEM bundle.
	// CAFile 用给定的 PEM 证书包替换系统根证书。
	CAFile string `yaml:"ca_file"`
	// CertFile and KeyFile enable mutual TLS. Set both or neither.
	// CertFile 与 KeyFile 启用双向 TLS，两者必须同时设置或同时留空。
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// ServerName overrides the name verified in the server certificate.
	// ServerName 覆盖校验服务端证书时使用的名称。
	ServerName string `yaml:"server_name"`
	// InsecureSkipVerify disables certificate verification. Only use it in
	// development: it makes the exporter trust any server.
	// InsecureSkipVerify 关闭证书校验，仅限开发环境使用：它会让 exporter
	// 信任任意服务端。
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
}

// ErrEndpointRequired is returned when Config.Endpoint is empty after defaults.
// ErrEndpointRequired 在应用默认值后 Config.Endpoint 仍为空时返回。
var ErrEndpointRequired = errors.New("otel: endpoint is required")

// Validate checks the configuration without opening a connection or reading
// certificate files. Empty values are accepted and receive defaults in New.
// Validate 只校验配置，不建立连接也不读取证书文件。空值允许通过，由 New
// 填入默认值。
func (c Config) Validate() error {
	switch strings.ToLower(strings.TrimSpace(c.Protocol)) {
	case "", ProtocolGRPC, ProtocolHTTP:
	default:
		return fmt.Errorf("otel: invalid protocol %q (want grpc or http)", c.Protocol)
	}
	switch strings.ToLower(strings.TrimSpace(c.Compression)) {
	case "", CompressionGzip, CompressionNone:
	default:
		return fmt.Errorf("otel: invalid compression %q (want gzip or none)", c.Compression)
	}
	switch strings.ToLower(strings.TrimSpace(c.Sampler)) {
	case "", SamplerParentBasedTraceIDRatio, SamplerParentBasedAlwaysOn,
		SamplerParentBasedAlwaysOff, SamplerTraceIDRatio, SamplerAlwaysOn, SamplerAlwaysOff:
	default:
		return fmt.Errorf("otel: invalid sampler %q", c.Sampler)
	}
	if c.SampleRatio < 0 || c.SampleRatio > 1 {
		return fmt.Errorf("otel: sample_ratio must be between 0 and 1, got %v", c.SampleRatio)
	}
	if endpoint := strings.TrimSpace(c.Endpoint); endpoint != "" {
		if strings.Contains(endpoint, "://") {
			u, err := url.Parse(endpoint)
			if err != nil {
				return fmt.Errorf("otel: invalid endpoint %q: %w", c.Endpoint, err)
			}
			if u.Scheme != "http" && u.Scheme != "https" {
				return fmt.Errorf("otel: invalid endpoint scheme %q (want http or https)", u.Scheme)
			}
			if strings.TrimSpace(u.Host) == "" {
				return fmt.Errorf("otel: endpoint %q has no host", c.Endpoint)
			}
		}
	}
	for key := range c.Headers {
		if strings.TrimSpace(key) == "" {
			return errors.New("otel: header names must not be empty")
		}
	}
	if c.Timeout < 0 {
		return errors.New("otel: timeout must not be negative")
	}
	if c.ShutdownTimeout < 0 {
		return errors.New("otel: shutdown_timeout must not be negative")
	}
	if err := c.Batch.Validate(); err != nil {
		return err
	}
	certSet := strings.TrimSpace(c.TLS.CertFile) != ""
	keySet := strings.TrimSpace(c.TLS.KeyFile) != ""
	if certSet != keySet {
		return errors.New("otel: tls cert_file and key_file must be set together")
	}
	if c.Insecure && (certSet || keySet || strings.TrimSpace(c.TLS.CAFile) != "" ||
		strings.TrimSpace(c.TLS.ServerName) != "" || c.TLS.InsecureSkipVerify) {
		return errors.New("otel: insecure cannot be combined with tls settings")
	}
	return nil
}

// Validate checks the batch settings.
// Validate 校验批处理参数，非法组合在构建 exporter 之前就会被拒绝。
func (b BatchConfig) Validate() error {
	if b.Timeout < 0 || b.ExportTimeout < 0 {
		return errors.New("otel: batch timeouts must not be negative")
	}
	if b.MaxQueueSize < 0 || b.MaxExportBatchSize < 0 {
		return errors.New("otel: batch sizes must not be negative")
	}
	if b.MaxQueueSize > 0 && b.MaxExportBatchSize > b.MaxQueueSize {
		return errors.New("otel: batch max_export_batch_size must not exceed max_queue_size")
	}
	return nil
}

// withDefaults returns a copy with defaults applied. It assumes the value has
// already passed Validate or that invalid values will be rejected again.
// withDefaults 返回填入默认值后的副本。它假定值已通过 Validate，或非法值会在
// 后续再次被拒绝。
func (c Config) withDefaults() Config {
	out := c
	out.Protocol = strings.ToLower(strings.TrimSpace(out.Protocol))
	if out.Protocol == "" {
		out.Protocol = ProtocolGRPC
	}
	out.Compression = strings.ToLower(strings.TrimSpace(out.Compression))
	if out.Compression == "" {
		out.Compression = CompressionGzip
	}
	out.Sampler = strings.ToLower(strings.TrimSpace(out.Sampler))
	if out.Sampler == "" {
		out.Sampler = SamplerParentBasedTraceIDRatio
	}
	if out.SampleRatio == 0 {
		out.SampleRatio = 1
	}
	if out.Timeout == 0 {
		out.Timeout = 10 * time.Second
	}
	if out.ShutdownTimeout == 0 {
		out.ShutdownTimeout = 5 * time.Second
	}
	out.ServiceName = strings.TrimSpace(out.ServiceName)
	out.ServiceVersion = strings.TrimSpace(out.ServiceVersion)
	out.Environment = strings.TrimSpace(out.Environment)
	out.Endpoint = strings.TrimSpace(out.Endpoint)
	if out.Endpoint == "" {
		if out.Protocol == ProtocolHTTP {
			out.Endpoint = "localhost:4318"
		} else {
			out.Endpoint = "localhost:4317"
		}
	}
	out.Batch = out.Batch.withDefaults()
	return out
}

func (b BatchConfig) withDefaults() BatchConfig {
	out := b
	if out.Timeout == 0 {
		out.Timeout = 5 * time.Second
	}
	if out.MaxQueueSize == 0 {
		out.MaxQueueSize = 2048
	}
	if out.MaxExportBatchSize == 0 {
		out.MaxExportBatchSize = 512
	}
	if out.ExportTimeout == 0 {
		out.ExportTimeout = 30 * time.Second
	}
	return out
}
