package otel

import (
	"context"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// TraceIDs returns the trace and span IDs stored in ctx, or empty strings when
// the context carries no valid span.
func TraceIDs(ctx context.Context) (traceID, spanID string) {
	if ctx == nil {
		return "", ""
	}
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return "", ""
	}
	return sc.TraceID().String(), sc.SpanID().String()
}

// TraceFields returns zap fields for the span in ctx. The result is nil when
// the context carries no valid span, so log records outside a trace stay
// unchanged.
func TraceFields(ctx context.Context) []zap.Field {
	traceID, spanID := TraceIDs(ctx)
	if traceID == "" {
		return nil
	}
	return []zap.Field{
		zap.String("trace_id", traceID),
		zap.String("span_id", spanID),
	}
}

// Logger returns base with trace_id and span_id fields when ctx is inside a
// sampled span. The original logger is returned otherwise.
func Logger(ctx context.Context, base *zap.Logger) *zap.Logger {
	if base == nil {
		base = zap.NewNop()
	}
	fields := TraceFields(ctx)
	if len(fields) == 0 {
		return base
	}
	return base.With(fields...)
}
