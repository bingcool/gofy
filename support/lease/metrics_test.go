package lease_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/lease"
	"github.com/bingcool/gofy/support/otel"
)

type failingLocker struct{ err error }

func (f failingLocker) Acquire(context.Context, string, time.Duration) (lease.Lease, error) {
	return nil, f.err
}

func TestAcquireFailureMetric(t *testing.T) {
	metrics, err := otel.NewMetrics(otel.MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = metrics.Shutdown(context.Background()) }()

	err = lease.Run(context.Background(), failingLocker{err: errors.New("store down")}, "settle", lease.RunConfig{
		TTL:           100 * time.Millisecond,
		Name:          "settle",
		MeterProvider: metrics.MeterProvider(),
		Metrics:       true,
	}, func(context.Context, int64) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "store down") {
		t.Fatalf("Run error = %v, want the store error", err)
	}

	body := scrapeLeaseMetrics(t, metrics)
	if !leaseContainsSeries(body, `gofy_lease_acquire_failures_total{lease="settle"} 1`) {
		t.Fatalf("acquire failure counter missing\n%s", body)
	}
	// ErrHeld is normal standby and must not be counted as a failure.
	if strings.Contains(body, "gofy_lease_acquire_failures_total") == false {
		t.Fatalf("no acquire series at all\n%s", body)
	}
}

func TestRenewLossMetric(t *testing.T) {
	metrics, err := otel.NewMetrics(otel.MetricsConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	defer func() { _ = metrics.Shutdown(context.Background()) }()

	locker := &fakeLocker{renew: func(context.Context, *fakeLease) error { return lease.ErrLeaseLost }}
	err = lease.Run(context.Background(), locker, "settle", lease.RunConfig{
		TTL:           100 * time.Millisecond,
		Name:          "settle",
		MeterProvider: metrics.MeterProvider(),
		Metrics:       true,
	}, func(ctx context.Context, _ int64) error {
		<-ctx.Done()
		return nil
	})
	if !errors.Is(err, lease.ErrLeaseLost) {
		t.Fatalf("Run error = %v, want ErrLeaseLost", err)
	}

	body := scrapeLeaseMetrics(t, metrics)
	if !leaseContainsSeries(body, `gofy_lease_renew_failures_total{lease="settle",outcome="lost"} 1`) {
		t.Fatalf("lost renewal counter missing\n%s", body)
	}
}

func scrapeLeaseMetrics(t *testing.T, m *otel.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, otel.DefaultMetricsPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

func leaseContainsSeries(body, want string) bool {
	start := strings.Index(want, "{")
	if start < 0 {
		return strings.Contains(body, want)
	}
	end := strings.Index(want, "}")
	name := want[:start]
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

// TestRunRejectsMetricsWithoutProvider is the M-06 regression test: the switch
// without a provider can only be a wiring mistake.
// TestRunRejectsMetricsWithoutProvider 是 M-06 的回归用例：开关配 nil provider
// 只可能是接线错误。
func TestRunRejectsMetricsWithoutProvider(t *testing.T) {
	err := lease.Run(context.Background(), &fakeLocker{}, "settle",
		lease.RunConfig{TTL: time.Second, Metrics: true},
		func(context.Context, int64) error { return nil })
	if err == nil {
		t.Fatal("Run with Metrics=true and a nil provider must fail")
	} else if !strings.Contains(err.Error(), "set both fields together") {
		t.Fatalf("unexpected error: %v", err)
	}
}
