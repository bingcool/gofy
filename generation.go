package gofy

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bingcool/gofy/support"
	gofydb "github.com/bingcool/gofy/support/db"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// generation is one published set of request-facing resources: the route table
// and everything Reload must release after the requests that use them finish.
//
// A request acquires the lease before entering handler and releases it when the
// handler returns. Reload publishes the new generation first, then retires the
// previous one: retired generations reject new leases, and their modules,
// database and logger are released only after the last in-flight request
// releases its lease.
// generation 是一代已发布的请求侧资源：路由表，以及 Reload 必须等使用它们的
// 请求结束后才能释放的所有东西。
// 请求进入 handler 前获取 lease，handler 返回后释放。Reload 先发布新一代，再
// 退役上一代：退役代拒绝新的 lease，其模块、数据库和 logger 要等最后一个在途
// 请求释放 lease 之后才释放。
type generation struct {
	id      uint64
	handler http.Handler
	lease   *generationLease

	// started holds the modules this generation started. Injected modules are
	// owned by the App and survive reloads, so they are not listed here.
	// started 记录本代启动的模块。注入的模块归 App 所有并跨 Reload 存活，所以
	// 不列在这里。
	started []support.Module

	// db, logger and their ownership flags let retirement skip resources that
	// the successor still uses (a reload without a new database shares the
	// pool).
	// db、logger 及其归属标记让退役流程跳过后继代仍在使用的资源（不带新数据库的
	// Reload 会共享同一个连接池）。
	db           *gorm.DB
	ownDB        bool
	logger       *zap.Logger
	systemLogger *zap.Logger
	accessLogger *zap.Logger
	ownLogger    bool
}

func newGeneration(id uint64, handler http.Handler) *generation {
	return &generation{
		id:      id,
		handler: handler,
		lease:   newGenerationLease(),
	}
}

// generationLease counts in-flight requests and signals when a retired
// generation has drained.
// generationLease 统计在途请求，并在退役代排空时发出信号。
type generationLease struct {
	mu      sync.Mutex
	active  int
	retired bool
	drained chan struct{}
	once    sync.Once
}

func newGenerationLease() *generationLease {
	return &generationLease{drained: make(chan struct{})}
}

// acquire reports whether the request may use this generation. It returns false
// once the generation is retired; the caller must retry with the current one.
// acquire 报告请求能否使用本代。代已退役时返回 false，调用方必须改用当前代重试。
func (l *generationLease) acquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.retired {
		return false
	}
	l.active++
	return true
}

func (l *generationLease) release() {
	l.mu.Lock()
	l.active--
	drained := l.retired && l.active == 0
	l.mu.Unlock()
	if drained {
		l.closeDrained()
	}
}

func (l *generationLease) retire() {
	l.mu.Lock()
	l.retired = true
	drained := l.active == 0
	l.mu.Unlock()
	if drained {
		l.closeDrained()
	}
}

func (l *generationLease) closeDrained() {
	l.once.Do(func() { close(l.drained) })
}

// wait blocks until every request has released the lease, the timeout expires,
// or ctx is cancelled. It reports whether the generation drained in time.
// wait 阻塞到所有请求释放 lease、超时或 ctx 被取消，并报告该代是否按时排空；
// timeout 非正数时按 10s 处理。
func (l *generationLease) wait(ctx context.Context, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-l.drained:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

// handlerSwap lets Reload replace the generation while the process keeps its
// listener.
// handlerSwap 让 Reload 在进程保留 listener 的情况下替换 generation。
type handlerSwap struct {
	inner     atomic.Pointer[generation]
	accepting *atomic.Bool
}

func newHandlerSwap(gen *generation, accepting *atomic.Bool) *handlerSwap {
	s := &handlerSwap{accepting: accepting}
	s.Store(gen)
	return s
}

func (s *handlerSwap) Store(gen *generation) { s.inner.Store(gen) }

func (s *handlerSwap) Load() *generation { return s.inner.Load() }

// ServeHTTP acquires a lease on the generation it loaded. If that generation
// was retired between the load and the acquire, it retries with the current
// one. A repeatedly failing acquire means a reload is racing this request; it
// is answered with 503 rather than entering a generation that is shutting down.
//
// The drain gate comes first: once StopAccepting cleared it, every new request
// gets 503 without entering a generation, while requests already inside one
// keep their lease and finish. Closing the listener instead would make Serve
// return and take the whole process out of the watcher's control.
// ServeHTTP 对它载入的 generation 获取 lease。若该代在载入与获取之间被退役，
// 就改用当前代重试；尝试多次仍获取失败说明有 Reload 与本次请求竞争，此时返回
// 503，而不是进入正在关闭的代。
//
// 摘流闸门检查放在最前面：StopAccepting 清零后，新请求不进入任何 generation，
// 直接返回 503；已经进入 generation 的请求仍持有 lease，可以继续跑完。这里
// 不关 listener —— 那会让 Serve 返回、整个进程脱离 watcher 的掌控。
func (s *handlerSwap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.accepting != nil && !s.accepting.Load() {
		w.Header().Set("Connection", "close")
		http.Error(w, "service is draining", http.StatusServiceUnavailable)
		return
	}
	for attempt := 0; attempt < 8; attempt++ {
		gen := s.inner.Load()
		if gen == nil {
			break
		}
		if !gen.lease.acquire() {
			continue
		}
		gen.handler.ServeHTTP(w, r)
		gen.lease.release()
		return
	}
	http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
}

// retireGeneration drains gen and then releases the resources it owns. It is
// called with the successor already published, so new requests never enter gen.
// retireGeneration 先排空 gen，再释放它持有的资源。调用时后继代已经发布，因此
// 新请求不会进入 gen。
func retireGeneration(ctx context.Context, gen, successor *generation, timeout time.Duration, lg *zap.Logger) []error {
	if gen == nil {
		return nil
	}
	gen.lease.retire()
	if !gen.lease.wait(ctx, timeout) {
		// The deadline is a safety valve: a stuck handler must not keep the
		// process from completing a reload forever. The error is reported so
		// operators can see requests were still running when resources closed.
		// 截止时间是安全阀：卡住的 handler 不能让进程永远完不成 Reload。这里
		// 记录告警，运维才能知道资源关闭时仍有请求在跑。
		lg.Warn("reload drain timed out: releasing the previous generation with requests in flight",
			zap.Uint64("generation", gen.id),
			zap.Duration("timeout", timeout),
		)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), timeout)
	errs := stopModuleList(stopCtx, gen.started, timeout)
	cancel()
	for _, err := range errs {
		lg.Error("module stop after reload failed", zap.Error(err))
	}

	if gen.ownDB && gen.db != nil && (successor == nil || gen.db != successor.db) {
		if err := gofydb.Close(gen.db); err != nil {
			lg.Error("close previous database after reload failed", zap.Error(err))
			errs = append(errs, err)
		}
	}
	if gen.ownLogger && gen.logger != nil && (successor == nil || gen.logger != successor.logger) {
		syncLoggers(gen.logger, gen.systemLogger, gen.accessLogger)
	}
	return errs
}
