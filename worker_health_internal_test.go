package gofy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bingcool/gofy/support/health"
)

// TestWorkerHealthLivenessIgnoresDrain pins the split the design asks for:
// liveness answers while the process is alive, so a clean shutdown is not
// restarted as a crash.
// TestWorkerHealthLivenessIgnoresDrain 固定设计要求的拆开语义：进程活着 liveness 就
// 应答，干净停机因此不会被当成崩溃重启。
func TestWorkerHealthLivenessIgnoresDrain(t *testing.T) {
	h := &workerHealth{}
	code := func() int {
		rec := httptest.NewRecorder()
		h.handleLive(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		return rec.Code
	}

	if got := code(); got != http.StatusOK {
		t.Fatalf("starting liveness = %d, want 200", got)
	}
	h.setReady()
	if got := code(); got != http.StatusOK {
		t.Fatalf("ready liveness = %d, want 200", got)
	}
	h.setStopping()
	if got := code(); got != http.StatusOK {
		t.Fatalf("draining liveness = %d, want 200", got)
	}
}

// TestWorkerHealthReadyzFollowsState pins readiness: 503 until the group
// reported, 200 while ready, 503 again once the drain begins. A buffered Ready
// signal must not turn the probe green after the stop started.
// TestWorkerHealthReadyzFollowsState 固定 readiness：分组上报前 503，就绪期间 200，
// drain 开始后再次 503。已经进入缓冲的 Ready 信号不能在停机之后把探针重新变绿。
func TestWorkerHealthReadyzFollowsState(t *testing.T) {
	h := &workerHealth{}
	code := func() int {
		rec := httptest.NewRecorder()
		h.handleReady(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}

	if got := code(); got != http.StatusServiceUnavailable {
		t.Fatalf("starting readiness = %d, want 503", got)
	}
	h.setReady()
	if got := code(); got != http.StatusOK {
		t.Fatalf("ready readiness = %d, want 200", got)
	}
	h.setStopping()
	if got := code(); got != http.StatusServiceUnavailable {
		t.Fatalf("draining readiness = %d, want 503", got)
	}
	h.setReady()
	if got := code(); got != http.StatusServiceUnavailable {
		t.Fatalf("readiness went back to ready after stopping: %d, want 503", got)
	}
}

// TestWorkerHealthReadyzConsultsRegistry pins that a failing readiness check
// keeps /readyz red even after the state reported ready. The cached 503 from
// before readiness must not hide the failure either.
// TestWorkerHealthReadyzConsultsRegistry 固定"readiness 检查失败时 /readyz 保持红色"，
// 即使状态已经上报就绪也不例外；就绪之前缓存的 503 也不能掩盖这次失败。
func TestWorkerHealthReadyzConsultsRegistry(t *testing.T) {
	reg := health.New(health.Options{})
	if err := reg.Register(health.Named("dependency", func(context.Context) error {
		return errors.New("down")
	})); err != nil {
		t.Fatalf("register: %v", err)
	}

	h := &workerHealth{reg: reg}
	h.setReady()

	rec := httptest.NewRecorder()
	h.handleReady(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("failing check = %d, want 503", rec.Code)
	}
}
