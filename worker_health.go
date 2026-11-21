package gofy

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/bingcool/gofy/support/health"
)

// workerHealth is the optional probe listener of a worker process. It answers two
// questions on two paths: /healthz says the process is alive, and /readyz says the
// group has connected and is not draining. Splitting them keeps a clean shutdown
// from looking like a crash: the liveness probe stays green while the process
// drains, and the readiness probe turns red so the scheduler sends no new work.
// workerHealth 是 worker 进程可选的探针监听。它在两个路径上回答两个问题：/healthz
// 表示进程活着，/readyz 表示分组已建连且未在 drain。拆开两者让干净停机不再像崩溃：
// liveness 探针在 drain 期间保持绿色，readiness 探针转红，调度器因此不再派新任务。
type workerHealth struct {
	srv   *http.Server
	state atomic.Int32

	// reg holds the worker's readiness checks: group readiness plus anything
	// registered with WithWorkerHealthChecks. A nil registry means the state alone
	// answers.
	// reg 持有 worker 的 readiness 检查：分组就绪加上 WithWorkerHealthChecks 注册的
	// 检查。nil 注册表表示只凭状态作答。
	reg *health.Registry
}

const (
	workerHealthStarting int32 = iota
	workerHealthReady
	workerHealthStopping
)

func newWorkerHealth(addr string, reg *health.Registry) *workerHealth {
	h := &workerHealth{reg: reg}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.handleLive)
	mux.HandleFunc("/readyz", h.handleReady)
	h.srv = &http.Server{
		Addr: addr,
		// A probe client that connects and stalls must not hold a goroutine
		// forever; the rest of the timeouts are irrelevant for a two-line
		// response.
		// 只连不收的探针客户端不能永远占住一个 goroutine；其余超时对两行响应没有意义。
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	return h
}

// start binds the address before serving, so a port clash fails startup instead
// of surfacing as a probe that never turns green.
// start 先绑定再提供服务：端口被占时在启动阶段就失败，而不是表现为探针永远不绿。
func (h *workerHealth) start() error {
	ln, err := net.Listen("tcp", h.srv.Addr)
	if err != nil {
		return err
	}
	go func() {
		// Serve returns ErrServerClosed on shutdown, which is the expected path.
		// Serve 在停机时返回 ErrServerClosed，这是预期路径。
		_ = h.srv.Serve(ln)
	}()
	return nil
}

// setReady moves the endpoint to ready only from starting. Once a stop has begun
// the state is final: a Ready signal that was already buffered must not turn the
// probe green again while the process is draining.
// setReady 只在 starting 时把端点置为 ready。停机一旦开始状态就是终态：已经进入缓冲的
// Ready 信号不能让 drain 中的探针重新变绿。
func (h *workerHealth) setReady() {
	h.state.CompareAndSwap(workerHealthStarting, workerHealthReady)
}

func (h *workerHealth) setStopping() { h.state.Store(workerHealthStopping) }

// stop shuts the endpoint down inside its own budget. It is safe to call twice.
// stop 在自己的预算内关闭端点，重复调用是安全的。
func (h *workerHealth) stop() {
	if h == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.srv.Shutdown(ctx); err != nil {
		_ = h.srv.Close()
	}
}

// handleLive answers liveness: the process is running and its listener is open.
// Drain state deliberately does not matter here, or a rollout would restart every
// replica as it shuts down cleanly.
// handleLive 应答 liveness：进程在运行且监听已打开。drain 状态刻意不参与判断，否则
// 每次滚动发布都会把正在干净停机的副本重启一遍。
func (h *workerHealth) handleLive(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// handleReady answers readiness: the state must be ready and every registered
// readiness check must pass. The state is checked before the registry on purpose:
// markReady closes a channel once and never reopens it, so a cached ready
// snapshot would otherwise keep answering 200 for the whole drain window.
// handleReady 应答 readiness：状态必须为 ready，且所有已注册 readiness 检查通过。
// 先看状态再看注册表是刻意的：markReady 关闭的通道只会关闭一次、不会重新打开，只查
// 注册表的话，缓存的就绪快照会让整个 drain 窗口一直返回 200。
func (h *workerHealth) handleReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if h.state.Load() != workerHealthReady {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
		return
	}
	if h.reg != nil && !h.reg.Ready(r.Context()).Ready {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}
