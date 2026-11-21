package gofy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWorkerHealthStateIsMonotonic pins the ordering the probe promises: once
// shutdown starts, a Ready signal that was already buffered cannot turn the
// probe green again. A scheduler that sees 200 during the drain window keeps
// sending work to a process that is leaving.
// TestWorkerHealthStateIsMonotonic 固定探针承诺的顺序：停机开始后，已经进入缓冲的
// Ready 信号不能让它重新变绿。调度器若在 drain 窗口看到 200，就会继续把任务发给一个
// 正在退出的进程。
func TestWorkerHealthStateIsMonotonic(t *testing.T) {
	h := &workerHealth{} // handle only reads the state, no listener needed
	code := func() int {
		rec := httptest.NewRecorder()
		h.handle(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		return rec.Code
	}

	if got := code(); got != http.StatusServiceUnavailable {
		t.Fatalf("starting probe = %d, want 503", got)
	}
	h.setReady()
	if got := code(); got != http.StatusOK {
		t.Fatalf("ready probe = %d, want 200", got)
	}
	h.setStopping()
	if got := code(); got != http.StatusServiceUnavailable {
		t.Fatalf("stopping probe = %d, want 503", got)
	}
	// The regression: a buffered Ready consumed after the stop began.
	// 回归点：停机开始之后才被消费的缓冲 Ready 信号。
	h.setReady()
	if got := code(); got != http.StatusServiceUnavailable {
		t.Fatalf("probe went back to ready after stopping: %d, want 503", got)
	}
}
