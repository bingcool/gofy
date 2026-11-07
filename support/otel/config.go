// Package otel provides production-oriented OpenTelemetry tracing for gofy:
// a validated configuration, an OTLP exporter with batch processing, W3C
// propagation and instrumentation helpers for Gin, net/http, GORM and Redis.
//
// The package owns no global state unless Config.Global is enabled. Callers
// receive a *Provider and decide when to shut it down.
package otel

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Exporter protocols.
const (
	ProtocolGRPC = "grpc"
	ProtocolHTTP = "http"
)

// Compression modes.
const (
	CompressionGzip = "gzip"
	CompressionNone = "none"
)

// Samplers accepted by Config.Sampler.
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
type Config struct {
	// Enabled turns tracing on. When false the framework installs no
	// middleware and builds no exporter.
	Enabled bool `yaml:"enabled"`

	// Endpoint is the OTLP collector address. With a scheme (http://host:port)
	// it is used as a URL; without one it is treated as host:port. The default
	// is localhost:4317 for gRPC and localhost:4318 for HTTP.
	Endpoint string `yaml:"endpoint"`

	// Protocol selects the OTLP transport: grpc or http.
	Protocol string `yaml:"protocol" default:"grpc"`

	// Insecure disables TLS, for collectors reached over a private network.
	Insecure bool `yaml:"insecure"`

	// Headers are sent with every export, for example an API token. Values are
	// never logged.
	Headers map[string]string `yaml:"headers"`

	// Compression is gzip or none.
	Compression string `yaml:"compression" default:"gzip"`

	// Timeout bounds one export attempt.
	Timeout time.Duration `yaml:"timeout" default:"10s"`

	// Sampler is the sampling strategy. The default keeps the decision of the
	// calling service and samples roots by SampleRatio.
	Sampler string `yaml:"sampler" default:"parentbased_traceidratio"`

	// SampleRatio samples that fraction of root traces, between 0 and 1.
	// Zero means the default (1.0); use sampler: always_off to disable
	// sampling explicitly.
	SampleRatio float64 `yaml:"sample_ratio" default:"1"`

	// ServiceName, ServiceVersion and Environment become the
	// service.name, service.version and deployment.environment.name resource
	// attributes. The framework fills empty values from the App name, build
	// version and environment.
	ServiceName    string `yaml:"service_name"`
	ServiceVersion string `yaml:"service_version"`
	Environment    string `yaml:"environment"`

	// Attributes are extra resource attributes. Keys equal to the ones derived
	// above are appended, so the last value wins in most backends.
	Attributes map[string]string `yaml:"attributes"`

	// Global installs the provider and propagator as the process-wide OTel
	// defaults, so third-party libraries can use them without being wired.
	// Leave it false when the process hosts more than one App.
	Global bool `yaml:"global"`

	// Batch controls the batch span processor.
	Batch BatchConfig `yaml:"batch"`

	// TLS configures the exporter connection when Insecure is false.
	TLS TLSConfig `yaml:"tls"`

	// ShutdownTimeout bounds the final flush during shutdown.
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout" default:"5s"`
}

// BatchConfig controls the batch span processor. All fields map directly to
// sdktrace.BatchSpanProcessorOption.
type BatchConfig struct {
	Timeout            time.Duration `yaml:"timeout" default:"5s"`
	MaxQueueSize       int           `yaml:"max_queue_size" default:"2048"`
	MaxExportBatchSize int           `yaml:"max_export_batch_size" default:"512"`
	ExportTimeout      time.Duration `yaml:"export_timeout" default:"30s"`
}

// TLSConfig configures the TLS client used by the OTLP exporter.
type TLSConfig struct {
	// CAFile replaces the system roots with the given PEM bundle.
	CAFile string `yaml:"ca_file"`
	// CertFile and KeyFile enable mutual TLS. Set both or neither.
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// ServerName overrides the name verified in the server certificate.
	ServerName string `yaml:"server_name"`
	// InsecureSkipVerify disables certificate verification. Only use it in
	// development: it makes the exporter trust any server.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
}

// ErrEndpointRequired is returned when Config.Endpoint is empty after defaults.
var ErrEndpointRequired = errors.New("otel: endpoint is required")

// Validate checks the configuration without opening a connection or reading
// certificate files. Empty values are accepted and receive defaults in New.
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
