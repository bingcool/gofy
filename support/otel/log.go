package otel

import (
	"context"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// TraceIDs returns the trace and span IDs stored in ctx, or empty strings when
// the context carries no valid span.
// TraceIDs 返回 ctx 中保存的 trace ID 与 span ID；上下文没有有效 Span 时返回
// 空字符串。
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
// TraceFields 返回 ctx 中 Span 对应的 zap 字段。上下文没有有效 Span 时返回
// nil，Trace 之外的日志记录因此保持不变。
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
// Logger 在 ctx 处于被采样的 Span 中时返回附带 trace_id、span_id 字段的 base；
// 否则原样返回。base 为 nil 时退回 no-op logger。
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
