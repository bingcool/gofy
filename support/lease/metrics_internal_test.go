package lease

import "testing"

func TestDisabledLeaseMetricsDoNotAllocate(t *testing.T) {
	m := &leaseMetrics{name: "settle"}
	allocs := testing.AllocsPerRun(200, func() {
		m.recordAcquireFailure()
		m.recordRenewFailure(false)
		m.recordRenewFailure(true)
	})
	if allocs != 0 {
		t.Fatalf("disabled metrics allocated %g times per call", allocs)
	}
}
