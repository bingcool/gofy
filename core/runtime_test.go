package core

import (
	"testing"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

func TestRuntimeMeterProviderDefaultsToNoop(t *testing.T) {
	rt := NewRuntime(RuntimeConfig{})
	if rt.MeterProvider == nil {
		t.Fatal("MeterProvider is nil")
	}
	if _, ok := rt.MeterProvider.(metricnoop.MeterProvider); !ok {
		t.Fatalf("MeterProvider = %T, want noop", rt.MeterProvider)
	}
	if rt.Metrics {
		t.Fatal("Metrics = true for a runtime with no metrics config")
	}
	meter := rt.MeterProvider.Meter("test")
	if _, err := meter.Int64Counter("gofy.test.counter"); err != nil {
		t.Fatalf("noop meter rejected an instrument: %v", err)
	}
}

func TestRuntimeMeterProviderIsPropagated(t *testing.T) {
	provider := metricnoop.NewMeterProvider()
	rt := NewRuntime(RuntimeConfig{MeterProvider: provider, Metrics: true})
	if rt.MeterProvider != provider {
		t.Fatal("MeterProvider was not copied into the runtime")
	}
	if !rt.Metrics {
		t.Fatal("Metrics flag was not copied into the runtime")
	}
}
