package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newTestProvider(t *testing.T, cfg Config) (*Provider, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	cfg.Enabled = true
	p, err := New(context.Background(), cfg,
		WithLogger(zap.NewNop()),
		WithSpanExporter(exporter),
		WithServiceName("test-service"),
		WithServiceVersion("1.2.3"),
		WithEnvironment("test"),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	return p, exporter
}

func TestNewDisabled(t *testing.T) {
	p, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if p.Enabled() {
		t.Fatal("disabled provider reports enabled")
	}
	if p.GinMiddleware(nil) != nil {
		t.Fatal("disabled provider must not install middleware")
	}
	if p.TracerProvider() == nil {
		t.Fatal("TracerProvider() must never be nil")
	}
	if p.Propagator() == nil {
		t.Fatal("Propagator() must never be nil")
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	if _, err := New(context.Background(), Config{Enabled: true, Protocol: "udp"}); err == nil {
		t.Fatal("New() = nil error, want validation error")
	}
}

func TestProviderResourceAndSpan(t *testing.T) {
	p, exporter := newTestProvider(t, Config{})
	_, span := p.Tracer("test").Start(context.Background(), "operation")
	span.End()
	if err := p.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	if spans[0].Name != "operation" {
		t.Fatalf("span name = %q, want operation", spans[0].Name)
	}
	res := spans[0].Resource
	if got := resourceValue(res, "service.name"); got != "test-service" {
		t.Fatalf("service.name = %q, want test-service", got)
	}
	if got := resourceValue(res, "service.version"); got != "1.2.3" {
		t.Fatalf("service.version = %q, want 1.2.3", got)
	}
	if got := resourceValue(res, "deployment.environment.name"); got != "test" {
		t.Fatalf("deployment.environment.name = %q, want test", got)
	}
}

func TestGinMiddlewarePropagatesTrace(t *testing.T) {
	p, exporter := newTestProvider(t, Config{})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(p.GinMiddleware(SkipPaths("/healthz")))
	var handlerTraceID string
	router.GET("/orders/:id", func(c *gin.Context) {
		handlerTraceID = trace.SpanContextFromContext(c.Request.Context()).TraceID().String()
		c.Status(http.StatusOK)
	})
	router.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })

	const incomingTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	req := httptest.NewRequest(http.MethodGet, "/orders/42", nil)
	req.Header.Set("traceparent", "00-"+incomingTraceID+"-00f067aa0ba902b7-01")
	router.ServeHTTP(httptest.NewRecorder(), req)
	if handlerTraceID != incomingTraceID {
		t.Fatalf("handler trace id = %q, want %q", handlerTraceID, incomingTraceID)
	}

	if err := p.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}
	if spans := exporter.GetSpans(); len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if err := p.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}
	if spans := exporter.GetSpans(); len(spans) != 1 {
		t.Fatalf("health check exported a span; total = %d, want 1", len(spans))
	}
}

func TestHTTPClientInjectsTraceparent(t *testing.T) {
	p, _ := newTestProvider(t, Config{})
	var header string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ctx, span := p.Tracer("test").Start(context.Background(), "parent")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	resp, err := p.HTTPClient(nil).Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = resp.Body.Close()
	span.End()
	if !strings.HasPrefix(header, "00-"+span.SpanContext().TraceID().String()+"-") {
		t.Fatalf("traceparent = %q, want trace id %s", header, span.SpanContext().TraceID())
	}
}

func TestGORMPluginRecordsSpan(t *testing.T) {
	p, exporter := newTestProvider(t, Config{})
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{
		DSN:                       "user:pass@tcp(127.0.0.1:3306)/test?parseTime=true",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DryRun:                 true,
		SkipDefaultTransaction: true,
		DisableAutomaticPing:   true,
		Logger:                 logger.Discard,
	})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}
	if err := p.InstrumentGORM(db); err != nil {
		t.Fatalf("InstrumentGORM() error = %v", err)
	}
	type order struct {
		ID   uint `gorm:"primarykey"`
		Name string
	}
	if err := db.Table("orders").Create(&order{Name: "demo"}).Error; err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := p.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	stub := spans[0]
	if stub.SpanKind != trace.SpanKindClient {
		t.Fatalf("span kind = %v, want client", stub.SpanKind)
	}
	if got := spanAttr(stub, "db.system"); got != "mysql" {
		t.Fatalf("db.system = %q, want mysql", got)
	}
	if got := spanAttr(stub, "db.operation"); got != "INSERT" {
		t.Fatalf("db.operation = %q, want INSERT", got)
	}
	if got := spanAttr(stub, "db.sql.table"); got != "orders" {
		t.Fatalf("db.sql.table = %q, want orders", got)
	}
	if got := spanAttr(stub, "db.statement"); !strings.Contains(got, "INSERT") {
		t.Fatalf("db.statement = %q, want an INSERT template", got)
	}
}

func TestInstrumentRedisRecordsSpan(t *testing.T) {
	p, exporter := newTestProvider(t, Config{})
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer client.Close()
	if err := p.InstrumentRedis(client); err != nil {
		t.Fatalf("InstrumentRedis() error = %v", err)
	}
	ctx := context.Background()
	if err := client.Set(ctx, "order:1", "paid", 0).Err(); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := p.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}
	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("no spans exported")
	}
	if got := spanAttr(spans[0], "db.system"); got != "redis" {
		t.Fatalf("db.system = %q, want redis", got)
	}
	for _, stub := range spans {
		if got := spanAttr(stub, "db.statement"); got != "" {
			t.Fatalf("db.statement = %q, want empty (arguments disabled)", got)
		}
	}
}

func TestTraceFields(t *testing.T) {
	p, _ := newTestProvider(t, Config{})
	ctx, span := p.Tracer("test").Start(context.Background(), "operation")
	defer span.End()

	traceID, spanID := TraceIDs(ctx)
	if traceID != span.SpanContext().TraceID().String() || spanID != span.SpanContext().SpanID().String() {
		t.Fatalf("TraceIDs() = %q/%q, want %q/%q",
			traceID, spanID, span.SpanContext().TraceID(), span.SpanContext().SpanID())
	}
	fields := TraceFields(ctx)
	if len(fields) != 2 {
		t.Fatalf("TraceFields() returned %d fields, want 2", len(fields))
	}
	if traceID, spanID := TraceIDs(context.Background()); traceID != "" || spanID != "" {
		t.Fatalf("TraceIDs(background) = %q/%q, want empty", traceID, spanID)
	}
}

func resourceValue(res interface{ Attributes() []attribute.KeyValue }, key string) string {
	for _, kv := range res.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsString()
		}
	}
	return ""
}

func spanAttr(stub tracetest.SpanStub, key string) string {
	for _, kv := range stub.Attributes {
		if string(kv.Key) == key {
			return kv.Value.AsString()
		}
	}
	return ""
}
