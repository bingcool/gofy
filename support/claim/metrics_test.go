package claim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/otel"
	"gorm.io/gorm"
)

func TestClaimMetricsRecordClaimedAndOutcomes(t *testing.T) {
	otelMetrics, err := otel.NewMetrics(otel.MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = otelMetrics.Shutdown(context.Background()) }()

	m, err := newClaimMetrics(otelMetrics.MeterProvider(), true, "order")
	if err != nil {
		t.Fatalf("newClaimMetrics: %v", err)
	}
	m.recordClaimed(3)
	for _, outcome := range []string{"succeeded", "failed", "dead", "lease_lost"} {
		m.recordOutcome(outcome, 20*time.Millisecond)
	}

	body := scrapeClaimMetrics(t, otelMetrics)
	for _, want := range []string{
		`gofy_claim_claimed_total{job="order"} 3`,
		`gofy_claim_outcomes_total{job="order",outcome="succeeded"} 1`,
		`gofy_claim_outcomes_total{job="order",outcome="failed"} 1`,
		`gofy_claim_outcomes_total{job="order",outcome="dead"} 1`,
		`gofy_claim_outcomes_total{job="order",outcome="lease_lost"} 1`,
		`gofy_claim_handler_duration_seconds_count{job="order",outcome="succeeded"} 1`,
	} {
		if !claimContainsSeries(body, want) {
			t.Fatalf("scrape is missing %q\n%s", want, body)
		}
	}
	// A lost lease has no handler duration: the handler never settled the row.
	if strings.Contains(body, `gofy_claim_handler_duration_seconds_count{job="order",outcome="lease_lost"}`) {
		t.Fatalf("lease loss produced a handler duration\n%s", body)
	}
}

func TestRunnerWiresClaimMetrics(t *testing.T) {
	otelMetrics, err := otel.NewMetrics(otel.MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = otelMetrics.Shutdown(context.Background()) }()

	handler := func(context.Context, []byte) (uint64, error) { return 0, nil }
	r, err := NewRunner(new(gorm.DB), RunnerConfig{
		Config:        Config{Table: "job_task", Job: "order"},
		Handler:       handler,
		MeterProvider: otelMetrics.MeterProvider(),
		Metrics:       true,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if !r.metrics.enabled || r.metrics.job != "order" {
		t.Fatalf("runner metrics = %+v, want enabled for job order", r.metrics)
	}

	plain, err := NewRunner(new(gorm.DB), RunnerConfig{
		Config:  Config{Table: "job_task", Job: "order"},
		Handler: handler,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if plain.metrics.enabled {
		t.Fatal("runner created instruments without metrics enabled")
	}
}

func TestDisabledClaimMetricsDoNotAllocate(t *testing.T) {
	m := &claimMetrics{job: "order"}
	allocs := testing.AllocsPerRun(200, func() {
		m.recordClaimed(3)
		m.recordOutcome("succeeded", time.Millisecond)
	})
	if allocs != 0 {
		t.Fatalf("disabled metrics allocated %g times per call", allocs)
	}
}

func scrapeClaimMetrics(t *testing.T, m *otel.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, otel.DefaultMetricsPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// claimContainsSeries ignores the exporter's extra otel_scope_* labels by
// checking each expected label as a substring of the same sample line.
func claimContainsSeries(body, want string) bool {
	start := strings.Index(want, "{")
	if start < 0 {
		return strings.Contains(body, want)
	}
	name := want[:start]
	end := strings.Index(want, "}")
	labels := strings.Split(strings.Trim(want[start+1:end], ","), ",")
	value := strings.TrimSpace(want[end+1:])
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+"{") || !strings.HasSuffix(line, " "+value) {
			continue
		}
		matched := true
		for _, label := range labels {
			if !strings.Contains(line, label) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// TestNewRunnerRejectsMetricsWithoutProvider is the M-06 regression test: the
// switch without a provider can only be a wiring mistake.
// TestNewRunnerRejectsMetricsWithoutProvider 是 M-06 的回归用例：开关配 nil provider
// 只可能是接线错误。
func TestNewRunnerRejectsMetricsWithoutProvider(t *testing.T) {
	_, err := NewRunner(new(gorm.DB), RunnerConfig{
		Config:  Config{Table: "job_task", Job: "order"},
		Handler: func(context.Context, []byte) (uint64, error) { return 0, nil },
		Metrics: true,
	})
	if err == nil {
		t.Fatal("NewRunner with Metrics=true and a nil provider must fail")
	} else if !strings.Contains(err.Error(), "set both fields together") {
		t.Fatalf("unexpected error: %v", err)
	}
}
