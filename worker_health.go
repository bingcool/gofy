package gofy

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// workerHealth is the optional /healthz endpoint of a worker process. It
// answers one question only: has this process started its group and is it not
// yet shutting down. Business health ("no messages for ten minutes") stays out
// of the probe on purpose, because a broker hiccup would then restart every
// replica at once.
// workerHealth 是 worker 进程可选的 /healthz 端点。它只回答一个问题：这个进程是否
// 已经启动分组、且尚未进入停机。业务健康（"十分钟没消费到消息"）故意不进探针，
// 否则一次 broker 抖动会让所有副本一起重启。
type workerHealth struct {
	srv   *http.Server
	state atomic.Int32
}

const (
	workerHealthStarting int32 = iota
	workerHealthReady
	workerHealthStopping
)

func newWorkerHealth(addr string) *workerHealth {
	h := &workerHealth{}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.handle)
	h.srv = &http.Server{
		Addr: addr,
		// A probe client that connects and stalls must not hold a goroutine
		// forever; the rest of the timeouts are irrelevant for a two-line
		// response.
		// 只连不收的探针客户端不能永远占住一个 goroutine；其余超时对两行响应没有
		// 意义。
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	return h
}

// start binds the address before serving, so a port clash fails startup
// instead of surfacing as a probe that never turns green.
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

func (h *workerHealth) setReady()    { h.state.Store(workerHealthReady) }
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

func (h *workerHealth) handle(w http.ResponseWriter, _ *http.Request) {
	if h.state.Load() == workerHealthReady {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("not ready\n"))
}
