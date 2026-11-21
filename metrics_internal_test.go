package gofy

import (
	"testing"

	"github.com/bingcool/gofy/support/diagnostics"
	"github.com/bingcool/gofy/support/otel"
	"go.uber.org/zap"
)

func TestWithMetricsIsIndependentOfDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
		want bool
	}{
		{"default", nil, false},
		{"legacy diagnostics", []Option{WithDiagnostics(diagnostics.Config{Enabled: true})}, true},
		{"diagnostics without metrics", []Option{WithDiagnostics(diagnostics.Config{Enabled: true, DisableMetrics: true})}, false},
		{"explicit metrics", []Option{WithMetrics(otel.MetricsConfig{Enabled: true})}, true},
		{"explicit off overrides diagnostics", []Option{
			WithDiagnostics(diagnostics.Config{Enabled: true}),
			WithMetrics(otel.MetricsConfig{Enabled: false}),
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, err := New(tt.opts...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			metrics, err := app.buildMetrics(zap.NewNop())
			if err != nil {
				t.Fatalf("buildMetrics: %v", err)
			}
			if got := metrics.Enabled(); got != tt.want {
				t.Fatalf("Enabled() = %v, want %v", got, tt.want)
			}
			if err := metrics.Shutdown(nil); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
		})
	}
}

func TestWithMetricsRejectsBadPath(t *testing.T) {
	if _, err := New(WithMetrics(otel.MetricsConfig{Enabled: true, Path: "metrics"})); err == nil {
		t.Fatal("New accepted a metrics path without a leading slash")
	}
}
