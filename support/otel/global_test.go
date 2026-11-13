package otel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// recordingHandler records the errors delivered to the OTel global error
// handler, proving the Provider did not replace the process-level ErrorHandler.
// recordingHandler 记录 OTel 全局错误处理器收到的错误，用于证明 Provider
// 没有替换进程级 ErrorHandler。
type recordingHandler struct {
	mu   sync.Mutex
	errs []error
}

func (h *recordingHandler) Handle(err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errs = append(h.errs, err)
}

func (h *recordingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.errs)
}

// failingExporter simulates an unreachable collector.
// failingExporter 模拟不可达的 collector。
type failingExporter struct {
	err error
}

func (e failingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return e.err
}

func (e failingExporter) Shutdown(context.Context) error { return nil }

// injectTraceparent returns the traceparent injected by the global propagator;
// an empty string means TraceContext is not installed.
// injectTraceparent 返回全局 propagator 注入的 traceparent；空字符串表示
// propagator 未安装 TraceContext。
func injectTraceparent(p propagation.TextMapPropagator) string {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01},
		SpanID:     trace.SpanID{0x02},
		TraceFlags: trace.FlagsSampled,
	})
	carrier := propagation.MapCarrier{}
	p.Inject(trace.ContextWithSpanContext(context.Background(), sc), carrier)
	return carrier["traceparent"]
}

// saveGlobals restores the OTel global state after the test so other tests in
// the same process are not affected.
// saveGlobals 在测试结束后恢复 OTel 全局状态，避免影响同进程的其他测试。
func saveGlobals(t *testing.T) {
	t.Helper()
	tp := otelapi.GetTracerProvider()
	prop := otelapi.GetTextMapPropagator()
	handler := otelapi.GetErrorHandler()
	t.Cleanup(func() {
		otelapi.SetTracerProvider(tp)
		otelapi.SetTextMapPropagator(prop)
		otelapi.SetErrorHandler(handler)
	})
}

// With Global=false, neither creating nor shutting down a Provider touches
// ErrorHandler, TracerProvider or Propagator.
// Global=false 时创建与关闭 Provider 都不改动 ErrorHandler、TracerProvider
// 和 Propagator。
func TestNonGlobalProviderLeavesProcessGlobalsUntouched(t *testing.T) {
	saveGlobals(t)
	sentinel := &recordingHandler{}
	otelapi.SetErrorHandler(sentinel)

	tpBefore := otelapi.GetTracerProvider()
	traceparentBefore := injectTraceparent(otelapi.GetTextMapPropagator())

	p, err := New(context.Background(), Config{Enabled: true, Global: false},
		WithSpanExporter(tracetest.NewInMemoryExporter()),
		WithLogger(zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := otelapi.GetTracerProvider(); got != tpBefore {
		t.Fatal("Global=false replaced the process tracer provider")
	}
	if got := injectTraceparent(otelapi.GetTextMapPropagator()); got != traceparentBefore {
		t.Fatalf("Global=false replaced the process propagator (traceparent=%q)", got)
	}
	if got := otelapi.GetErrorHandler(); got != sentinel {
		t.Fatal("Global=false replaced the process error handler")
	}
	otelapi.GetErrorHandler().Handle(errors.New("sentinel"))
	if sentinel.count() != 1 {
		t.Fatalf("sentinel handler calls = %d, want 1", sentinel.count())
	}

	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := otelapi.GetTracerProvider(); got != tpBefore {
		t.Fatal("Shutdown restored or replaced the process tracer provider")
	}
	if got := otelapi.GetErrorHandler(); got != sentinel {
		t.Fatal("Shutdown replaced the process error handler")
	}
}

// Two Global=false Providers coexist in one process: both export normally,
// export failures are logged to their own loggers, and the one-minute dedup
// still applies.
// 两个 Global=false Provider 共存在同一进程：都能正常导出，导出失败分别
// 记录到各自的 logger，并且一分钟去重仍然生效。
func TestNonGlobalProvidersLogExportFailuresIndependently(t *testing.T) {
	saveGlobals(t)
	coreA, logsA := observer.New(zapcore.WarnLevel)
	coreB, logsB := observer.New(zapcore.WarnLevel)
	errExport := errors.New("collector unreachable")

	newProvider := func(core zapcore.Core) *Provider {
		p, err := New(context.Background(), Config{Enabled: true, Global: false},
			WithSpanExporter(failingExporter{err: errExport}),
			WithLogger(zap.New(core)),
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return p
	}
	pA := newProvider(coreA)
	pB := newProvider(coreB)
	t.Cleanup(func() {
		_ = pA.Shutdown(context.Background())
		_ = pB.Shutdown(context.Background())
	})

	export := func(p *Provider) {
		_, span := p.Tracer("test").Start(context.Background(), "operation")
		span.End()
		_ = p.ForceFlush(context.Background())
	}
	export(pA)
	export(pA) // the second failure within a minute must be deduplicated; 一分钟内第二次失败应被去重
	export(pB)

	entriesA := logsA.All()
	if len(entriesA) != 1 {
		t.Fatalf("provider A logs = %d, want 1 (deduplicated)", len(entriesA))
	}
	if entriesA[0].Message != "opentelemetry error" {
		t.Fatalf("provider A message = %q", entriesA[0].Message)
	}
	if got := entriesA[0].ContextMap()["error"]; got == nil {
		t.Fatalf("provider A log has no error field: %v", entriesA[0].Context)
	}
	entriesB := logsB.All()
	if len(entriesB) != 1 {
		t.Fatalf("provider B logs = %d, want 1", len(entriesB))
	}
}

// With Global=true all four process-level settings are installed together: the
// global Tracer uses this Provider's SDK, the global propagator injects
// traceparent, and the global ErrorHandler reports to this Provider's logger
// instead of the previous handler.
// Global=true 时四项进程级设置一起安装：全局 Tracer 走本 Provider 的 SDK，
// 全局 propagator 注入 traceparent，全局 ErrorHandler 记录到本 Provider
// 的 logger 而不是之前的处理器。
func TestGlobalProviderInstallsProcessGlobals(t *testing.T) {
	saveGlobals(t)
	sentinel := &recordingHandler{}
	otelapi.SetErrorHandler(sentinel)

	core, logs := observer.New(zapcore.WarnLevel)
	exporter := tracetest.NewInMemoryExporter()
	p, err := New(context.Background(), Config{Enabled: true, Global: true},
		WithSpanExporter(exporter),
		WithLogger(zap.New(core)),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	_, span := otelapi.Tracer("global-check").Start(context.Background(), "global-span")
	span.End()
	if err := p.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}
	if got := len(exporter.GetSpans()); got != 1 {
		t.Fatalf("global tracer exported %d spans, want 1", got)
	}
	if got := injectTraceparent(otelapi.GetTextMapPropagator()); got == "" {
		t.Fatal("global propagator does not inject traceparent")
	}
	otelapi.GetErrorHandler().Handle(errors.New("boom"))
	if sentinel.count() != 0 {
		t.Fatal("global error handler still forwards to the previous handler")
	}
	entries := logs.All()
	if len(entries) != 1 || entries[0].Message != "opentelemetry error" {
		t.Fatalf("provider logger entries = %v", entries)
	}

	globalTP := otelapi.GetTracerProvider()
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := otelapi.GetTracerProvider(); got != globalTP {
		t.Fatal("Shutdown replaced the global tracer provider installed by Global=true")
	}
}

// The one-minute dedup applies to the errorHandler directly: repeated errors
// inside the window only increment suppressed, and after the window one entry
// is logged again carrying the suppressed count.
// 一分钟去重直接作用于 errorHandler：窗口内的重复错误只累加 suppressed，
// 窗口过去后带 suppressed 计数再记录一次。
func TestErrorHandlerDedupesWithinMinute(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	handler := &errorHandler{logger: zap.New(core)}

	handler.Handle(errors.New("first"))
	handler.Handle(errors.New("second"))
	if got := len(logs.All()); got != 1 {
		t.Fatalf("entries within window = %d, want 1", got)
	}

	handler.mu.Lock()
	handler.last = time.Now().Add(-2 * time.Minute)
	handler.mu.Unlock()
	handler.Handle(errors.New("third"))

	entries := logs.All()
	if len(entries) != 2 {
		t.Fatalf("entries after window = %d, want 2", len(entries))
	}
	if got := entries[1].ContextMap()["suppressed"]; got != int64(1) {
		t.Fatalf("suppressed = %v, want 1", got)
	}
}
