package gofy

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHandlerPanicDoesNotLeakLease covers the case the audit named: with the
// default Recovery on, a business panic never reaches ServeHTTP, but
// http.ErrAbortHandler passes through it, and with Recovery disabled every
// panic does. Either way the lease must be released, or each later Reload
// would wait out a full drain timeout before it could release that generation.
// TestHandlerPanicDoesNotLeakLease 覆盖审计指出的场景：默认开着 Recovery 时业务
// panic 到不了 ServeHTTP，但 http.ErrAbortHandler 会穿过它；关掉 Recovery 后所有
// panic 都会。两种情况下 lease 都必须释放，否则之后每次 Reload 都要等满一个 drain
// 超时才能释放该代。
func TestHandlerPanicDoesNotLeakLease(t *testing.T) {
	for _, abort := range []bool{true, false} {
		name := "business panic"
		if abort {
			name = "http.ErrAbortHandler"
		}
		t.Run(name, func(t *testing.T) {
			gen := newGeneration(1, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				if abort {
					panic(http.ErrAbortHandler)
				}
				panic("boom")
			}))
			swap := newHandlerSwap(gen, newAdmissionGate())

			func() {
				// The server recovers panics in its connection goroutine; here the
				// test plays that role, so the assertion can run afterwards.
				// server 在连接 goroutine 里 recover panic；这里由测试扮演那个角色，
				// 断言因此能在之后执行。
				defer func() { _ = recover() }()
				swap.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
			}()

			gen.lease.mu.Lock()
			active := gen.lease.active
			gen.lease.mu.Unlock()
			if active != 0 {
				t.Fatalf("active leases after panic = %d, want 0", active)
			}
			// A leaked lease of a retired generation would also stall Reload, so
			// check the generation is still usable rather than only the counter.
			// 退役代上泄漏的 lease 同样会拖住 Reload，因此除了计数还要确认该代仍可用。
			if !gen.lease.acquire() {
				t.Fatal("generation refused a lease after a panicking handler")
			}
			gen.lease.release()
		})
	}
}

// TestGateClosedStopsNewRequestsFromEntering is the second acceptance test of
// P2-10: once close returns, no further request may reach a handler. The old
// implementation checked the flag before acquiring the lease, so a request that
// read "open" just before the flag flipped still entered.
// TestGateClosedStopsNewRequestsFromEntering 是 P2-10 的第二个验收测试：close 返回
// 之后不能有任何新请求进入 handler。旧实现在获取 lease 之前检查标志，因此刚读到
// "打开"、随后标志翻转的请求仍会进入。
func TestGateClosedStopsNewRequestsFromEntering(t *testing.T) {
	var entered atomic.Int32
	gate := newAdmissionGate()
	gen := newGeneration(1, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	swap := newHandlerSwap(gen, gate)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for {
				select {
				case <-stop:
					return
				default:
				}
				rec := httptest.NewRecorder()
				swap.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
					t.Errorf("unexpected status %d", rec.Code)
					return
				}
			}
		}()
	}

	// Let the workers actually run, then close the gate. close waits for every
	// admission already in flight, so the counter read right after it returns is
	// the final value.
	// 先让 worker 真正跑起来，再关闭闸门。close 会等所有已经进入的准入决定完成，
	// 因此它返回后立刻读到的计数就是最终值。
	time.Sleep(20 * time.Millisecond)
	gate.close()
	settled := entered.Load()
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()

	if got := entered.Load(); got != settled {
		t.Fatalf("handler entries grew from %d to %d after the gate closed", settled, got)
	}
	if settled == 0 {
		t.Fatal("no request entered before the gate closed; the test proved nothing")
	}
}

// TestGateResumeLetsRequestsInAgain proves the gate is reversible: a restart
// that could not be performed leaves the process serving, and a gate that could
// not be reopened would make the resume path useless.
// TestGateResumeLetsRequestsInAgain 证明闸门可逆：没能执行的重启会让进程继续服务，
// 而打不开的闸门会让恢复路径失去意义。
func TestGateResumeLetsRequestsInAgain(t *testing.T) {
	gate := newAdmissionGate()
	gen := newGeneration(1, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	swap := newHandlerSwap(gen, gate)

	gate.close()
	if rec := httptest.NewRecorder(); func() bool {
		swap.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec.Code == http.StatusServiceUnavailable
	}() {
		t.Log("gate closed as expected")
	} else {
		t.Fatalf("status while closed = %d, want 503", rec.Code)
	}

	if !gate.resume() {
		t.Fatal("resume reported no state change on a closed gate")
	}
	rec := httptest.NewRecorder()
	swap.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status after resume = %d, want 200", rec.Code)
	}
	if gate.resume() {
		t.Fatal("resume reported a state change on an open gate")
	}
}
