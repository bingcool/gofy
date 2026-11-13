package gofy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/logger"
	"github.com/bingcool/gofy/support"
	gofydb "github.com/bingcool/gofy/support/db"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ReloadSpec describes what Reload replaces. Nil fields keep their current
// value.
//
// The listener is never rebound: HTTP host, port and server timeouts keep
// their startup values, so changing them requires a restart. The same applies
// to OpenTelemetry: the tracer provider and its exporter stay alive across
// reloads, and a new telemetry configuration requires a process restart.
type ReloadSpec struct {
	// Env replaces the environment reported by the Runtime and the App.
	Env string

	// Log replaces the logger. A logger injected with WithLogger is replaced
	// too and is not synced afterwards, because the App does not own it.
	Log *logger.Config

	// Database reconnects the MySQL pool. When set, the App owns the new
	// connection and closes the previous one after the old modules stop.
	Database *gofydb.Config

	// ModuleFactories rebuild the modules registered through
	// WithModuleFactories, and with them the routes. Nil rebuilds from the
	// factories passed to New.
	ModuleFactories []support.ModuleFactory

	// MiddlewareRegistrars rebuild the application middleware registry. Nil
	// reuses the registrars passed to New. They run against a fresh registry on
	// every reload, so configuration that feeds a middleware (for example a new
	// JWT secret) takes effect without a restart.
	MiddlewareRegistrars []MiddlewareRegistrar
}

// Reload applies a new configuration to a running App without exiting the
// process. It rebuilds the logger, the database pool, the middleware registry
// and the modules, starts the new modules, swaps the HTTP handler atomically
// and then stops the old modules.
//
// New modules start before the old ones stop, so requests are served
// throughout the swap. The overhead is a short window where both module sets
// are alive; modules that hold an exclusive resource (a socket, a lock file)
// should not be rebuilt this way without stopping the old instance first.
//
// Modules passed to WithModules are reused: they stay running and are only
// re-registered on the new router, because Reload cannot rebuild instances it
// did not create. Their Runtime is whatever they captured at construction.
//
// The middleware registry is rebuilt from the registrars, but middleware
// installed directly on the engine by options (CORS, rate limit, JWT, and so
// on) keeps its startup behavior. Reload never changes the listening address
// or the pid file; those require a restart.
//
// A reload proceeds in five steps: validate the spec, build the new logger and
// DB pool, rebuild and start the new modules, publish the new state and
// handler, then stop the old modules. A failure in the first three steps
// discards what was built and leaves the current app serving.
func (a *App) Reload(ctx context.Context, spec ReloadSpec) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Validate first: a bad spec must never disturb the running app.
	if spec.Log != nil {
		if err := spec.Log.Validate(); err != nil {
			return fmt.Errorf("gofy: reload log config: %w", err)
		}
	}
	if spec.Database != nil {
		if err := spec.Database.Validate(); err != nil {
			return fmt.Errorf("gofy: reload database config: %w", err)
		}
	}

	// Serialize with cleanup and with other reloads.
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()

	// Snapshot the current state and release a.mu: rebuilding below is slow
	// (logger, DB handshake, module factories) and must not block requests.
	a.mu.Lock()
	if a.state != stateRunning || a.handlerRef == nil || a.rt == nil {
		a.mu.Unlock()
		return fmt.Errorf("%w: reload requires a running server", ErrNotRunning)
	}
	oldGen := a.gen
	if oldGen == nil {
		a.mu.Unlock()
		return fmt.Errorf("%w: reload requires a running server", ErrNotRunning)
	}
	env := a.opts.env
	lg, ownLogger := a.logger, a.ownLogger
	systemLogger := a.systemLogger
	db, ownDB := a.db, a.ownDB
	startedAt := a.rt.StartedAt
	runCtx := a.rt.Context()
	factories := spec.ModuleFactories
	if factories == nil {
		factories = a.opts.moduleFactories
	}
	registrars := spec.MiddlewareRegistrars
	if registrars == nil {
		registrars = a.opts.middlewareRegistrars
	}
	injected := append([]support.Module(nil), a.opts.modules...)
	// Module names are unique within a build (buildModulesWith rejects
	// duplicates), so the name identifies an injected instance across reloads
	// without comparing interface values.
	injectedNames := make(map[string]struct{}, len(injected))
	for _, m := range injected {
		injectedNames[m.Name()] = struct{}{}
	}
	injectedStarted := make([]support.Module, 0, len(injected))
	for _, m := range a.started {
		if _, ok := injectedNames[m.Name()]; ok {
			// Injected instances keep running across the swap; remember them
			// so Shutdown still stops them.
			injectedStarted = append(injectedStarted, m)
		}
	}
	timeout := a.opts.shutdownTimeout
	a.mu.Unlock()

	if v := strings.TrimSpace(spec.Env); v != "" {
		env = v
	}

	// Nil spec fields reuse the current logger/database. Anything built here is
	// owned by the App and released by discard on a later failure.
	rebuiltLogger := false
	if spec.Log != nil {
		newLogger, err := logger.New(*spec.Log)
		if err != nil {
			return fmt.Errorf("gofy: reload logger: %w", err)
		}
		newSystem, err := logger.NewSystem(*spec.Log)
		if err != nil {
			_ = logger.Sync(newLogger)
			return fmt.Errorf("gofy: reload system logger: %w", err)
		}
		if len(spec.Log.SystemErrorOutputs) == 0 {
			newSystem = newLogger
		}
		lg, systemLogger, ownLogger, rebuiltLogger = newLogger, newSystem, true, true
	}

	rebuiltDB := false
	if spec.Database != nil {
		newDB, err := gofydb.New(ctx, *spec.Database, lg)
		if err != nil {
			discardReloadResources(rebuiltDB, rebuiltLogger, db, lg, systemLogger)
			return fmt.Errorf("gofy: reload database: %w", err)
		}
		db, ownDB, rebuiltDB = newDB, true, true
	}
	discard := func() { discardReloadResources(rebuiltDB, rebuiltLogger, db, lg, systemLogger) }

	rt := core.NewRuntime(core.RuntimeConfig{
		Name:           a.opts.name,
		Env:            env,
		Logger:         lg,
		DB:             db,
		Version:        a.version,
		StartedAt:      startedAt,
		Ctx:            runCtx,
		Fail:           func(err error) { a.cancel(err) },
		TracerProvider: a.telemetry.TracerProvider(),
		Propagator:     a.telemetry.Propagator(),
		Tracing:        a.telemetry.Enabled(),
	})
	engine, modules, err := a.buildHTTPWith(rt, lg, systemLogger, factories, injected, registrars)
	if err != nil {
		discard()
		return err
	}

	// Start the new modules before publishing them: if any of them fails, the
	// old routes and modules are still the live ones.
	started := make([]support.Module, 0, len(modules))
	for _, m := range modules {
		// Injected instances were started at startup and keep running.
		if _, ok := injectedNames[m.Name()]; ok {
			continue
		}
		starter, ok := m.(support.Starter)
		if !ok {
			continue
		}
		if err := starter.Start(rt.Context()); err != nil {
			stopModuleList(rt.Context(), started, timeout)
			discard()
			return fmt.Errorf("gofy: reload module %q start: %w", m.Name(), err)
		}
		started = append(started, m)
	}
	allStarted := make([]support.Module, 0, len(started)+len(injectedStarted))
	allStarted = append(allStarted, started...)
	allStarted = append(allStarted, injectedStarted...)

	// Every replacement resource is ready, so publish all of it in one critical
	// section; readers never see a half-swapped app.
	a.mu.Lock()
	if a.state != stateRunning {
		a.mu.Unlock()
		stopModuleList(rt.Context(), started, timeout)
		discard()
		return fmt.Errorf("%w: reload requires a running server", ErrNotRunning)
	}
	a.opts.env = env
	if spec.Log != nil {
		a.opts.logConfig = *spec.Log
	}
	if spec.Database != nil {
		cfg := *spec.Database
		a.opts.dbConfig = &cfg
	}
	if spec.ModuleFactories != nil {
		a.opts.moduleFactories = append([]support.ModuleFactory(nil), spec.ModuleFactories...)
	}
	if spec.MiddlewareRegistrars != nil {
		a.opts.middlewareRegistrars = append([]MiddlewareRegistrar(nil), spec.MiddlewareRegistrars...)
	}
	a.logger, a.ownLogger = lg, ownLogger
	a.systemLogger = systemLogger
	a.db, a.ownDB = db, ownDB
	a.rt = rt
	a.modules = modules
	a.started = allStarted

	newGen := newGeneration(oldGen.id+1, engine)
	newGen.started = started
	newGen.db, newGen.ownDB = db, ownDB
	newGen.logger, newGen.systemLogger, newGen.ownLogger = lg, systemLogger, ownLogger
	a.gen = newGen
	a.handlerRef.Store(newGen)
	a.mu.Unlock()

	// Requests already in flight on the old generation keep their lease; new
	// requests enter the new one. The old modules and resources are released
	// only after those requests finish (or the drain timeout expires).
	errs := retireGeneration(context.Background(), oldGen, newGen, timeout, lg)

	lg.Info("application reloaded",
		zap.String("env", env),
		zap.Bool("logger_rebuilt", rebuiltLogger),
		zap.Bool("database_rebuilt", rebuiltDB),
		zap.Int("modules", len(modules)),
	)
	return errors.Join(errs...)
}

// discardReloadResources releases only resources this Reload built, so a
// rejected reload never closes the live database or syncs the live logger.
func discardReloadResources(rebuiltDB, rebuiltLogger bool, db *gorm.DB, lg, system *zap.Logger) {
	if rebuiltDB && db != nil {
		_ = gofydb.Close(db)
	}
	if rebuiltLogger {
		_ = logger.Sync(lg)
		if system != nil && system != lg {
			_ = logger.Sync(system)
		}
	}
}

// stopModuleList stops modules in reverse registration order, like cleanup,
// and gives each Stop its own timeout so one hung module cannot block the
// others.
func stopModuleList(ctx context.Context, modules []support.Module, timeout time.Duration) []error {
	if len(modules) == 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	var errs []error
	for i := len(modules) - 1; i >= 0; i-- {
		stopper, ok := modules[i].(support.Stopper)
		if !ok {
			continue
		}
		stopCtx, cancel := context.WithTimeout(ctx, timeout)
		err := stopper.Stop(stopCtx)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("gofy: module %q stop: %w", modules[i].Name(), err))
		}
	}
	return errs
}
