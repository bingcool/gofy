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

	// bind pins this generation's module state into the request context. It is
	// composed from the request binders of the modules started for this
	// generation, so the state becomes visible exactly when the handler does.
	// bind 把本代模块状态固定进请求上下文。它由本代启动的模块 binder 组合而成，
	// 因此状态的可见时刻与 handler 完全一致。
	bind func(ctx context.Context) context.Context

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

// composeRequestBinders merges the binders of one generation's started modules
// into a single function. Order follows module registration order, so a module
// registered later sees the context its predecessor produced.
//
// It returns nil when no module participates, which keeps the request path free
// of an extra context copy for applications that do not need the hook.
// composeRequestBinders 把一代已启动模块的 binder 合并成一个函数，顺序沿用模块
// 注册顺序，后注册的模块看到的是前一个模块产出的上下文。
//
// 没有任何模块参与时返回 nil，不需要这个钩子的应用因此不会在请求路径上多一次
// context 复制。
func composeRequestBinders(modules []support.Module) func(context.Context) context.Context {
	var binders []support.RequestBinderFunc
	for _, m := range modules {
		binder, ok := m.(support.RequestBinder)
		if !ok {
			continue
		}
		if fn := binder.RequestBinder(); fn != nil {
			binders = append(binders, fn)
		}
	}
	if len(binders) == 0 {
		return nil
	}
	return func(ctx context.Context) context.Context {
		if ctx == nil {
			ctx = context.Background()
		}
		for _, fn := range binders {
			ctx = fn(ctx)
		}
		return ctx
	}
}

// handlerSwap lets Reload replace the generation while the process keeps its
// listener.
// handlerSwap 让 Reload 在进程保留 listener 的情况下替换 generation。
type handlerSwap struct {
	inner atomic.Pointer[generation]
	gate  *admissionGate
}

// newHandlerSwap builds the wrapper the server keeps for its whole life. A nil
// gate becomes a fresh open one, so a test can build the wrapper without caring
// about the lifecycle.
// newHandlerSwap 构造 server 整个生命周期持有的包装器。gate 为 nil 时新建一个
// 打开状态的闸门，测试因此不必关心生命周期就能构造它。
func newHandlerSwap(gen *generation, gate *admissionGate) *handlerSwap {
	if gate == nil {
		gate = newAdmissionGate()
	}
	s := &handlerSwap{gate: gate}
	s.Store(gen)
	return s
}

func (s *handlerSwap) Store(gen *generation) { s.inner.Store(gen) }

func (s *handlerSwap) Load() *generation { return s.inner.Load() }

// ServeHTTP admits the request through the gate, which both checks the drain
// state and acquires the lease on the generation the request loaded. A retired
// generation makes the caller retry with the current one; a closed gate answers
// 503 without entering any generation.
//
// The generation's binder runs after the lease is held: a request enters the
// generation whose lease it owns, and reads that generation's module state -
// never the state a half-finished reload has already installed globally.
// ServeHTTP 让请求穿过闸门：一步完成摘流状态检查与本代 lease 的获取。代已退役时
// 调用方改用当前代重试；闸门已关闭时不进入任何代，直接返回 503。
//
// 本代的 binder 在持有 lease 之后执行：请求进入的是它持有 lease 的那一代，读到
// 的也是那一代的模块状态，绝不会读到半途完成的 Reload 已经装到全局的新状态。
func (s *handlerSwap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for attempt := 0; attempt < 8; attempt++ {
		gen := s.inner.Load()
		if gen == nil {
			break
		}
		switch s.gate.admit(gen.lease) {
		case admitClosed:
			w.Header().Set("Connection", "close")
			http.Error(w, "service is draining", http.StatusServiceUnavailable)
			return
		case admitRetired:
			// A reload is swapping generations right now; load the current one.
			// 此刻有 Reload 正在换代，重新载入当前代。
			continue
		}
		s.serve(w, r, gen)
		return
	}
	http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
}

// serve runs one generation whose lease admission already took. The release is
// deferred on purpose: a handler that panics — including http.ErrAbortHandler,
// which the server turns into a connection abort — must not leave the counter
// elevated, or every later Reload would wait out a full drain timeout before it
// can release that generation.
// serve 运行已由准入流程取得 lease 的那一代。release 刻意用 defer：panic 的
// handler（包括 http.ErrAbortHandler，server 会把它变成连接中断）不能把计数留在
// 高位，否则之后每次 Reload 都要等满一个 drain 超时才能释放该代。
func (s *handlerSwap) serve(w http.ResponseWriter, r *http.Request, gen *generation) {
	defer gen.lease.release()
	if gen.bind != nil {
		r = r.WithContext(gen.bind(r.Context()))
	}
	gen.handler.ServeHTTP(w, r)
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
