package inflight

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

type markerError struct{}

func (markerError) Error() string { return "in flight" }
func (markerError) InFlight()     {}

// TestIsFindsWrappedMarker pins the structural match transports rely on: a
// handler error wrapped with context still counts as in flight.
// TestIsFindsWrappedMarker 固定各传输依赖的结构匹配：带上下文包装后的 handler
// 错误仍然算在途。
func TestIsFindsWrappedMarker(t *testing.T) {
	if !Is(fmt.Errorf("message 7: %w", markerError{})) {
		t.Fatal("wrapped in-flight error was not recognized")
	}
	if Is(errors.New("ordinary failure")) {
		t.Fatal("ordinary error was treated as in flight")
	}
}

// TestDelayFallsBackToDefault pins the zero-value contract used by every
// transport option.
// TestDelayFallsBackToDefault 固定每个传输选项都依赖的零值约定。
func TestDelayFallsBackToDefault(t *testing.T) {
	if got := Delay(0); got != DefaultDelay {
		t.Fatalf("Delay(0) = %s, want %s", got, DefaultDelay)
	}
	if got := Delay(-time.Second); got != DefaultDelay {
		t.Fatalf("Delay(-1s) = %s, want %s", got, DefaultDelay)
	}
	if got := Delay(time.Minute); got != time.Minute {
		t.Fatalf("Delay(1m) = %s", got)
	}
}
