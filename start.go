package gofy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/internal/pidfile"
	"github.com/bingcool/gofy/internal/process"
	"github.com/bingcool/gofy/logging"
	"github.com/bingcool/gofy/support/mysql"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/bingcool/gofy/transport"
	"github.com/bingcool/gofy/transport/middleware"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Run starts the HTTP server and blocks until the context is cancelled, the
// listener fails, or a fatal runtime error is reported.
func (a *App) Run(ctx context.Context) error {
	if err := a.beginStart(false); err != nil {
		return err
	}
	return a.run(ctx)
}

// Handler builds and returns an http.Handler without listening on a port.
// Modules are registered but not started; call Shutdown to release resources.
func (a *App) Handler(ctx context.Context) (http.Handler, error) {
	if err := a.beginStart(true); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	a.cancel = cancel
	a.setState(stateRunning)

	lg, system, own, err := a.buildLoggers()
	if err != nil {
		return nil, a.abort(err)
	}
	a.setLogger(lg, system, own)

	db, ownDB, err := a.buildDB(runCtx, lg)
	if err != nil {
		return nil, a.abort(err)
	}
	a.db, a.ownDB = db, ownDB

	rt := core.NewRuntime(core.RuntimeConfig{
		Name:    a.opts.name,
		Env:     a.opts.env,
		Logger:  lg,
		DB:      db,
		Version: a.version,
		Ctx:     runCtx,
		Fail:    func(err error) { cancel(err) },
	})
	a.rt = rt

	engine, modules, err := a.buildHTTP(rt, lg, system)
	if err != nil {
		return nil, a.abort(err)
	}
	a.modules = modules
	a.markReady()
	return engine, nil
}

func (a *App) run(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	a.cancel = cancel

	lg, system, own, err := a.buildLoggers()
	if err != nil {
		return a.abort(err)
	}
	a.setLogger(lg, system, own)

	db, ownDB, err := a.buildDB(ctx, lg)
	if err != nil {
		return a.abort(err)
	}
	a.db, a.ownDB = db, ownDB

	rt := core.NewRuntime(core.RuntimeConfig{
		Name:    a.opts.name,
		Env:     a.opts.env,
		Logger:  lg,
		DB:      db,
		Version: a.version,
		Ctx:     ctx,
		Fail:    func(err error) { cancel(err) },
	})
	a.rt = rt

	engine, modules, err := a.buildHTTP(rt, lg, system)
	if err != nil {
		return a.abort(err)
	}
	a.modules = modules
	// The server keeps this wrapper for its whole life; Reload only swaps the
	// handler stored inside it, so the listener is never touched.
	a.handlerRef = newHandlerSwap(engine)

	pidPath, err := a.resolvePIDPath()
	if err != nil {
		return a.abort(err)
	}
	if pidPath != "" {
		if existing, readErr := pidfile.Read(pidPath); readErr == nil && process.Alive(existing) {
			return a.abort(fmt.Errorf("%w: pid %d (%s)", ErrAlreadyRunning, existing, pidPath))
		}
	}

	addr := net.JoinHostPort(a.opts.http.Host, strconv.Itoa(a.opts.http.Port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return a.abort(fmt.Errorf("gofy: listen %s: %w", addr, err))
	}
	a.listener = listener

	if pidPath != "" {
		if err := pidfile.Write(pidPath, os.Getpid(), a.opts.pidPerm); err != nil {
			return a.abort(err)
		}
		a.pidPath = pidPath
		a.startPIDRefresh(lg)
	}

	if err := a.startModules(rt); err != nil {
		return a.abort(err)
	}
	a.setState(stateRunning)

	srv := a.newServer(a.handlerRef, lg)
	if a.opts.serverConf != nil {
		if err := a.opts.serverConf(srv); err != nil {
			return a.abort(fmt.Errorf("gofy: server configurator: %w", err))
		}
	}
	a.srv = srv
	a.serving = true
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(listener)
	}()

	// Register only once the listener is accepting connections, so discovery
	// clients never receive traffic for a port that is not ready.
	if err := a.startNacosRegistration(ctx, listener, lg); err != nil {
		return a.abort(err)
	}

	lg.Info("application started",
		zap.String("name", a.opts.name),
		zap.String("env", a.opts.env),
		zap.String("addr", addr),
		zap.String("version", a.version.Version),
	)
	a.printStartup(addr)
	a.markReady()

	var reason error
	select {
	case <-parent.Done():
	case <-a.stopCh:
	case <-rt.Context().Done():
		if cause := context.Cause(rt.Context()); cause != nil && !errors.Is(cause, context.Canceled) {
			reason = cause
		}
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			reason = fmt.Errorf("gofy: serve: %w", err)
		}
	}

	if cerr := a.cleanup(); cerr != nil {
		if reason == nil {
			reason = cerr
		} else {
			reason = errors.Join(reason, cerr)
		}
	}
	if reason == nil {
		lg.Info("application stopped")
	} else {
		lg.Error("application stopped with error", zap.Error(reason))
	}
	return reason
}

func (a *App) buildHTTP(rt *core.Runtime, lg, system *zap.Logger) (*gin.Engine, []transport.Module, error) {
	return a.buildHTTPWith(rt, lg, system, a.opts.moduleFactories, a.opts.modules)
}

func (a *App) buildHTTPWith(rt *core.Runtime, lg, system *zap.Logger, factories []transport.ModuleFactory, injected []transport.Module) (*gin.Engine, []transport.Module, error) {
	gin.SetMode(ginMode(a.opts.http.Mode))
	engine := gin.New()
	if len(a.opts.trusted) > 0 {
		if err := engine.SetTrustedProxies(a.opts.trusted); err != nil {
			return nil, nil, fmt.Errorf("gofy: trusted proxies: %w", err)
		}
	} else {
		// An empty slice disables gin's default "trust every proxy" behavior.
		if err := engine.SetTrustedProxies([]string{}); err != nil {
			return nil, nil, fmt.Errorf("gofy: disable trusted proxies: %w", err)
		}
	}

	engine.Use(middleware.RequestID(transport.DefaultRequestIDHeader))
	if a.opts.accessLog {
		engine.Use(middleware.AccessLog(lg, middleware.DefaultAccessLogOptions()))
	}
	if a.opts.cors != nil {
		corsMW, err := middleware.CORS(*a.opts.cors)
		if err != nil {
			return nil, nil, err
		}
		if corsMW != nil {
			engine.Use(corsMW)
		}
	}
	if a.opts.recovery {
		engine.Use(middleware.Recovery(system, a.opts.responder))
	}
	engine.Use(middleware.BodyLimit(a.opts.maxBody))
	if len(a.opts.middleware) > 0 {
		engine.Use(a.opts.middleware...)
	}
	if a.opts.health {
		registerHealth(engine, rt)
	}

	base := transport.NewRouter(engine, transport.RouterConfig{
		Prefix:    a.opts.apiPrefix,
		Responder: a.opts.responder,
		Validator: transport.NewValidator(),
	})
	modules, err := a.buildModulesWith(rt, factories, injected)
	if err != nil {
		return nil, nil, err
	}
	if err := a.registerModules(base, modules); err != nil {
		return nil, nil, err
	}
	return engine, modules, nil
}

func (a *App) buildModules(rt *core.Runtime) ([]transport.Module, error) {
	return a.buildModulesWith(rt, a.opts.moduleFactories, a.opts.modules)
}

func (a *App) buildModulesWith(rt *core.Runtime, factories []transport.ModuleFactory, injected []transport.Module) ([]transport.Module, error) {
	modules := make([]transport.Module, 0, len(factories)+len(injected))
	seen := make(map[string]struct{}, cap(modules))

	add := func(m transport.Module) error {
		name := strings.TrimSpace(m.Name())
		if !namePattern.MatchString(name) {
			return fmt.Errorf("gofy: invalid module name %q", name)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("gofy: duplicate module %q", name)
		}
		seen[name] = struct{}{}
		modules = append(modules, m)
		return nil
	}

	for i, factory := range factories {
		m, err := callFactory(factory, rt)
		if err != nil {
			return nil, fmt.Errorf("gofy: module factory %d: %w", i, err)
		}
		if m == nil {
			return nil, fmt.Errorf("gofy: module factory %d returned nil", i)
		}
		if err := add(m); err != nil {
			return nil, err
		}
	}
	for _, m := range injected {
		if err := add(m); err != nil {
			return nil, err
		}
	}
	return modules, nil
}

func callFactory(factory transport.ModuleFactory, rt *core.Runtime) (m transport.Module, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic: %v", rec)
		}
	}()
	return factory(rt)
}

func (a *App) registerModules(base *transport.Router, modules []transport.Module) error {
	used := make(map[string]string, len(modules))
	for _, m := range modules {
		prefix := "/" + m.Name()
		if p, ok := m.(transport.Prefixer); ok {
			prefix = p.Prefix()
		}
		prefix = transport.NormalizePrefix(prefix)
		full := transport.NormalizePrefix(a.opts.apiPrefix + prefix)
		if other, ok := used[full]; ok {
			return fmt.Errorf("gofy: route prefix %q is used by both %q and %q", full, other, m.Name())
		}
		used[full] = m.Name()
		if err := registerModule(m, base.Group(prefix)); err != nil {
			return err
		}
	}
	return nil
}

func registerModule(m transport.Module, r *transport.Router) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("gofy: module %q register panic: %v", m.Name(), rec)
		}
	}()
	if err := m.Register(r); err != nil {
		return fmt.Errorf("gofy: module %q register: %w", m.Name(), err)
	}
	return nil
}

func (a *App) startModules(rt *core.Runtime) error {
	for _, m := range a.modules {
		starter, ok := m.(transport.Starter)
		if !ok {
			continue
		}
		if err := starter.Start(rt.Context()); err != nil {
			return fmt.Errorf("gofy: module %q start: %w", m.Name(), err)
		}
		a.started = append(a.started, m)
	}
	return nil
}

// startNacosRegistration publishes this instance once the listener is up. A
// registration failure aborts startup: running without being discoverable is
// worse than failing fast.
func (a *App) startNacosRegistration(ctx context.Context, listener net.Listener, lg *zap.Logger) error {
	cfg := a.opts.nacosRegistration
	if cfg == nil || !cfg.Enabled || !cfg.Register.Enabled {
		return nil
	}
	regCfg := *cfg
	if regCfg.Register.Port == 0 {
		regCfg.Register.Port = listenerPort(listener, a.opts.http.Port)
	}
	registrar, err := nacos.NewRegistrar(regCfg, lg)
	if err != nil {
		return fmt.Errorf("gofy: nacos service registration: %w", err)
	}
	if err := registrar.Start(ctx); err != nil {
		return fmt.Errorf("gofy: nacos service registration: %w", err)
	}
	a.mu.Lock()
	a.registrar = registrar
	a.mu.Unlock()
	return nil
}

func listenerPort(listener net.Listener, fallback int) int {
	if listener != nil {
		if addr, ok := listener.Addr().(*net.TCPAddr); ok && addr.Port > 0 {
			return addr.Port
		}
	}
	return fallback
}

func (a *App) newServer(handler http.Handler, lg *zap.Logger) *http.Server {
	cfg := a.opts.http
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		ErrorLog:          zap.NewStdLog(lg),
	}
}

func (a *App) resolvePIDPath() (string, error) {
	if a.opts.pidFile == "" {
		if a.opts.workDir != "" {
			if _, err := a.resolveWorkDir(); err != nil {
				return "", err
			}
		}
		return "", nil
	}
	workDir, err := a.resolveWorkDir()
	if err != nil {
		return "", err
	}
	return pidfile.Resolve(a.opts.pidFile, workDir), nil
}

func (a *App) startPIDRefresh(lg *zap.Logger) {
	if a.opts.pidRefresh <= 0 {
		return
	}
	ticker := time.NewTicker(a.opts.pidRefresh)
	done := make(chan struct{})
	a.pidTicker = ticker
	a.pidTickerDone = done
	path := a.pidPath
	perm := a.opts.pidPerm
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := pidfile.Write(path, os.Getpid(), perm); err != nil {
					lg.Warn("refresh pid file failed", zap.String("path", path), zap.Error(err))
				}
			}
		}
	}()
}

func registerHealth(engine *gin.Engine, rt *core.Runtime) {
	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	engine.GET("/readyz", func(c *gin.Context) {
		if rt.DB == nil {
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		sqlDB, err := rt.DB.DB()
		if err != nil || sqlDB.PingContext(ctx) != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
}

// Shutdown triggers a graceful stop. It is idempotent and safe to call
// concurrently. Calling it before the app starts is a no-op.
func (a *App) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	switch a.getState() {
	case stateNew:
		return nil
	case stateStopped, stateFailed:
		return a.cleanupErr
	}

	a.stopOnce.Do(func() { close(a.stopCh) })

	a.mu.Lock()
	handler := a.handlerMode
	a.mu.Unlock()
	if handler {
		select {
		case <-a.ready:
			if a.getState() == stateFailed {
				return a.cleanupErr
			}
			return a.cleanup()
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	select {
	case <-a.done:
		return a.cleanupErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *App) abort(err error) error {
	_ = a.cleanup()
	a.setState(stateFailed)
	a.markReady()
	return err
}

// cleanup runs once and releases every resource the App owns.
func (a *App) cleanup() error {
	a.cleanupOnce.Do(func() {
		// Wait for an in-flight Reload: it may still be swapping modules and
		// resources, and cleanup must see the final state.
		a.reloadMu.Lock()
		defer a.reloadMu.Unlock()

		timeout := a.opts.shutdownTimeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		lg := a.Logger()
		var errs []error

		// Deregister before closing the server so discovery clients stop
		// routing new requests here while in-flight ones finish.
		if a.registrar != nil {
			if err := a.registrar.Stop(ctx); err != nil {
				lg.Warn("nacos service deregistration failed", zap.Error(err))
			}
			a.registrar = nil
		}

		if a.srv != nil && a.serving {
			if err := a.srv.Shutdown(ctx); err != nil {
				_ = a.srv.Close()
				errs = append(errs, fmt.Errorf("gofy: http shutdown: %w", err))
			}
		}
		if a.listener != nil && !a.serving {
			if err := a.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, fmt.Errorf("gofy: close listener: %w", err))
			}
		}

		for i := len(a.started) - 1; i >= 0; i-- {
			m := a.started[i]
			stopper, ok := m.(transport.Stopper)
			if !ok {
				continue
			}
			if err := stopper.Stop(ctx); err != nil {
				lg.Error("module stop failed", zap.String("module", m.Name()), zap.Error(err))
				errs = append(errs, fmt.Errorf("gofy: module %q stop: %w", m.Name(), err))
			}
		}

		if a.ownDB && a.db != nil {
			if err := mysql.Close(a.db); err != nil {
				lg.Error("close database failed", zap.Error(err))
				errs = append(errs, err)
			}
		}

		if a.cancel != nil {
			a.cancel(nil)
		}
		if a.pidTicker != nil {
			a.pidTicker.Stop()
			close(a.pidTickerDone)
		}
		if a.pidPath != "" {
			if err := pidfile.RemoveIfOwner(a.pidPath, os.Getpid()); err != nil {
				lg.Warn("remove pid file failed", zap.String("path", a.pidPath), zap.Error(err))
			}
		}
		if a.ownLogger {
			_ = logging.Sync(a.logger)
			if a.systemLogger != nil && a.systemLogger != a.logger {
				_ = logging.Sync(a.systemLogger)
			}
		}

		a.cleanupErr = errors.Join(errs...)
		a.setState(stateStopped)
		close(a.done)
	})
	return a.cleanupErr
}

func ginMode(mode string) string {
	switch strings.ToLower(mode) {
	case "debug":
		return gin.DebugMode
	case "test":
		return gin.TestMode
	default:
		return gin.ReleaseMode
	}
}
