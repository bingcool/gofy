package gofy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bingcool/gofy/support/health"
	"go.uber.org/zap"
)

// TestHealthDetailRendersChecks pins the diagnostic body: one endpoint answers
// "may I take traffic" and "which dependency is degraded" together, with the
// class and status an operator needs. The endpoint always answers 200 — it
// describes state, and no load balancer reads it.
// TestHealthDetailRendersChecks 固定诊断响应体：一个端点同时回答"能否接活"与"哪个依赖
// 降级"，并带上运维需要的类别与状态。该端点始终返回 200——它描述状态，没有负载均衡器读它。
func TestHealthDetailRendersChecks(t *testing.T) {
	app, err := New(WithName("health-detail"), WithLogger(zap.NewNop()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	app.opts.healthChecks = []healthCheckSpec{
		{
			checker: health.Named("database", func(context.Context) error {
				return errors.New("ping timeout")
			}),
			class: health.ClassReadiness,
		},
		{
			checker: health.Named("report", func(context.Context) error {
				return errors.New("report database is down")
			}),
			class: health.ClassDiagnostic,
		},
	}
	app.healthRegistry.Store(app.newHealthRegistry(nil, zap.NewNop()))

	rec := httptest.NewRecorder()
	app.healthDetailHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/health status = %d, want 200", rec.Code)
	}

	var body healthBodyJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body.Status != "unavailable" || body.Ready {
		t.Fatalf("status/ready = %q/%v, want unavailable/false", body.Status, body.Ready)
	}
	if len(body.Checks) != 2 {
		t.Fatalf("checks = %d, want 2", len(body.Checks))
	}

	byName := make(map[string]healthCheckJSON, len(body.Checks))
	for _, c := range body.Checks {
		byName[c.Name] = c
	}
	failed, ok := byName["database"]
	if !ok {
		t.Fatalf("database check missing from %+v", body.Checks)
	}
	if failed.Class != "readiness" || failed.Status != "failed" || failed.Error == "" {
		t.Fatalf("database check = %+v, want readiness/failed with an error", failed)
	}
	degraded, ok := byName["report"]
	if !ok {
		t.Fatalf("report check missing from %+v", body.Checks)
	}
	if degraded.Class != "diagnostic" || degraded.Status != "unknown" {
		t.Fatalf("report check = %+v, want diagnostic/unknown", degraded)
	}
}
