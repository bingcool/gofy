package gofy

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bingcool/gofy/logger"
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
type generation struct {
	id      uint64
	handler http.Handler
	lease   *generationLease

	// started holds the modules this generation started. Injected modules are
	// owned by the App and survive reloads, so they are not listed here.
	started []support.Module

	// db, logger and their ownership flags let retirement skip resources that
	// the successor still uses (a reload without a new database shares the
	// pool).
	db           *gorm.DB
	ownDB        bool
	logger       *zap.Logger
	systemLogger *zap.Logger
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
type handlerSwap struct {
	inner atomic.Pointer[generation]
}

func newHandlerSwap(gen *generation) *handlerSwap {
	s := &handlerSwap{}
	s.Store(gen)
	return s
}

func (s *handlerSwap) Store(gen *generation) { s.inner.Store(gen) }

func (s *handlerSwap) Load() *generation { return s.inner.Load() }

// ServeHTTP acquires a lease on the generation it loaded. If that generation
// was retired between the load and the acquire, it retries with the current
// one. A repeatedly failing acquire means a reload is racing this request; it
// is answered with 503 rather than entering a generation that is shutting down.
func (s *handlerSwap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
func retireGeneration(ctx context.Context, gen, successor *generation, timeout time.Duration, lg *zap.Logger) []error {
	if gen == nil {
		return nil
	}
	gen.lease.retire()
	if !gen.lease.wait(ctx, timeout) {
		// The deadline is a safety valve: a stuck handler must not keep the
		// process from completing a reload forever. The error is reported so
		// operators can see requests were still running when resources closed.
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
		_ = logger.Sync(gen.logger)
		if gen.systemLogger != nil && gen.systemLogger != gen.logger {
			_ = logger.Sync(gen.systemLogger)
		}
	}
	return errs
}
