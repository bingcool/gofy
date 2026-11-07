package otel

import (
	"testing"
	"time"
)

func TestConfigDefaults(t *testing.T) {
	cfg := Config{Enabled: true}.withDefaults()
	if cfg.Protocol != ProtocolGRPC {
		t.Fatalf("protocol = %q, want grpc", cfg.Protocol)
	}
	if cfg.Endpoint != "localhost:4317" {
		t.Fatalf("endpoint = %q, want localhost:4317", cfg.Endpoint)
	}
	if cfg.Compression != CompressionGzip {
		t.Fatalf("compression = %q, want gzip", cfg.Compression)
	}
	if cfg.Sampler != SamplerParentBasedTraceIDRatio {
		t.Fatalf("sampler = %q, want %q", cfg.Sampler, SamplerParentBasedTraceIDRatio)
	}
	if cfg.SampleRatio != 1 {
		t.Fatalf("sample ratio = %v, want 1", cfg.SampleRatio)
	}
	if cfg.Timeout != 10*time.Second || cfg.ShutdownTimeout != 5*time.Second {
		t.Fatalf("timeouts = %v/%v, want 10s/5s", cfg.Timeout, cfg.ShutdownTimeout)
	}
	if cfg.Batch.Timeout != 5*time.Second ||
		cfg.Batch.MaxQueueSize != 2048 ||
		cfg.Batch.MaxExportBatchSize != 512 ||
		cfg.Batch.ExportTimeout != 30*time.Second {
		t.Fatalf("unexpected batch defaults: %+v", cfg.Batch)
	}
}

func TestConfigDefaultsHTTP(t *testing.T) {
	cfg := Config{Enabled: true, Protocol: ProtocolHTTP}.withDefaults()
	if cfg.Endpoint != "localhost:4318" {
		t.Fatalf("endpoint = %q, want localhost:4318", cfg.Endpoint)
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"protocol", Config{Protocol: "udp"}},
		{"compression", Config{Compression: "zstd"}},
		{"sampler", Config{Sampler: "random"}},
		{"ratio too high", Config{SampleRatio: 1.5}},
		{"ratio negative", Config{SampleRatio: -0.1}},
		{"endpoint scheme", Config{Endpoint: "ftp://collector:4317"}},
		{"endpoint host", Config{Endpoint: "https://"}},
		{"empty header", Config{Headers: map[string]string{" ": "value"}}},
		{"negative timeout", Config{Timeout: -time.Second}},
		{"negative shutdown timeout", Config{ShutdownTimeout: -time.Second}},
		{"cert without key", Config{TLS: TLSConfig{CertFile: "client.pem"}}},
		{"insecure with tls", Config{Insecure: true, TLS: TLSConfig{CAFile: "ca.pem"}}},
		{"batch order", Config{Batch: BatchConfig{MaxQueueSize: 10, MaxExportBatchSize: 20}}},
		{"batch negative", Config{Batch: BatchConfig{Timeout: -time.Second}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
		})
	}
}

func TestConfigValidateAcceptsDefaults(t *testing.T) {
	if err := (Config{}).Validate(); err != nil {
		t.Fatalf("zero Config should be valid: %v", err)
	}
	valid := Config{
		Enabled:     true,
		Endpoint:    "https://collector.example.com:4318",
		Protocol:    ProtocolHTTP,
		Compression: CompressionNone,
		Sampler:     SamplerAlwaysOn,
		SampleRatio: 0.25,
		Headers:     map[string]string{"Authorization": "Bearer value"},
		TLS:         TLSConfig{CAFile: "ca.pem"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}
