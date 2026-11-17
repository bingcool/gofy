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
// ReloadSpec 描述 Reload 会替换哪些东西，字段为 nil 表示保持当前值。
// listener 从不重新绑定：HTTP host、端口和 server 超时沿用启动时的值，改动它们
// 必须重启进程。OpenTelemetry 同理：tracer provider 与 exporter 跨 Reload 存活，
// 新的 telemetry 配置也需要重启进程。
type ReloadSpec struct {
	// Env replaces the environment reported by the Runtime and the App.
	// Env 替换 Runtime 与 App 上报的环境标记。
	Env string

	// Log replaces the logger. A logger injected with WithLogger is replaced
	// too and is not synced afterwards, because the App does not own it.
	// Log 替换 logger。经 WithLogger 注入的 logger 也会被替换，且替换后不再 Sync，
	// 因为 App 并不拥有它的生命周期。
	Log *logger.Config

	// Database reconnects the MySQL pool. When set, the App owns the new
	// connection and closes the previous one after the old modules stop.
	// Database 重连 MySQL 连接池。设置后新连接归 App 所有，旧连接要等旧模块停止
	// 之后才关闭。
	Database *gofydb.Config

	// ModuleFactories rebuild the modules registered through
	// WithModuleFactories, and with them the routes. Nil rebuilds from the
	// factories passed to New.
	// ModuleFactories 重建经 WithModuleFactories 注册的模块，路由随之重建。nil 表示
	// 用传给 New 的工厂重建。
	ModuleFactories []support.ModuleFactory

	// MiddlewareRegistrars rebuild the application middleware registry. Nil
	// reuses the registrars passed to New. They run against a fresh registry on
	// every reload, so configuration that feeds a middleware (for example a new
	// JWT secret) takes effect without a restart.
	// MiddlewareRegistrars 重建应用层中间件注册表。nil 表示沿用传给 New 的注册函数。
	// 它们每次 Reload 都面对一个全新的注册表，因此喂给中间件的配置（例如新的 JWT
	// secret）无需重启即可生效。
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
// Reload 在不退出进程的前提下把新配置应用到运行中的 App：重建 logger、数据库
// 连接池、中间件注册表与模块，启动新模块，原子替换 HTTP handler，最后停止旧模块。
// 新模块先启动、旧模块后停止，切换过程中请求始终有人处理；代价是短暂存在两套
// 模块并存的窗口，持有独占资源（socket、锁文件）的模块不应这样重建，除非先停掉
// 旧实例。
// 经 WithModules 传入的模块会被复用：它们保持运行，只在新 Router 上重新注册，
// 因为 Reload 无法重建不是自己创建的实例，其 Runtime 仍是构造时捕获的那份。
// 中间件注册表由注册函数重建，但通过 Option 直接装在 engine 上的中间件（CORS、
// 限流、JWT 等）保持启动时的行为。Reload 从不改变监听地址或 pid 文件，这些改动
// 需要重启。
// 一次 Reload 分五步：校验 spec、构建新 logger 与 DB 连接池、重建并启动新模块、
// 发布新状态与 handler、停止旧模块。前三步失败会丢弃已构建的东西，当前 App 继续
// 提供服务。
func (a *App) Reload(ctx context.Context, spec ReloadSpec) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Validate first: a bad spec must never disturb the running app.
	// 先校验：非法的 spec 绝不能打扰正在运行的 App。
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
	// 与清理流程、以及其他 Reload 串行。
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()

	// Snapshot the current state and release a.mu: rebuilding below is slow
	// (logger, DB handshake, module factories) and must not block requests.
	// 快照当前状态后释放 a.mu：下面的重建（logger、DB 握手、模块工厂）很慢，
	// 不能阻塞请求。
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
	accessLogger := a.accessLogger
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
	// 模块名在一次构建内唯一（buildModulesWith 拒绝重名），因此跨 Reload 用名字
	// 就能认出注入的实例，无需比较接口值。
	injectedNames := make(map[string]struct{}, len(injected))
	for _, m := range injected {
		injectedNames[m.Name()] = struct{}{}
	}
	injectedStarted := make([]support.Module, 0, len(injected))
	for _, m := range a.started {
		if _, ok := injectedNames[m.Name()]; ok {
			// Injected instances keep running across the swap; remember them
			// so Shutdown still stops them.
			// 注入的实例在切换中保持运行；记下它们，Shutdown 才能照常停止。
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
	// spec 中为 nil 的字段沿用当前 logger / 数据库。这里新建的资源归 App 所有，
	// 后续步骤失败时由 discard 释放。
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
		newAccess, err := logger.NewAccess(*spec.Log)
		if err != nil {
			syncLoggers(newLogger, newSystem)
			return fmt.Errorf("gofy: reload access logger: %w", err)
		}
		if len(spec.Log.AccessOutputs) == 0 {
			newAccess = newLogger
		}
		lg, systemLogger, accessLogger, ownLogger, rebuiltLogger = newLogger, newSystem, newAccess, true, true
	}

	rebuiltDB := false
	if spec.Database != nil {
		newDB, err := gofydb.New(ctx, *spec.Database, lg)
		if err != nil {
			discardReloadResources(rebuiltDB, rebuiltLogger, db, lg, systemLogger, accessLogger)
			return fmt.Errorf("gofy: reload database: %w", err)
		}
		db, ownDB, rebuiltDB = newDB, true, true
	}
	discard := func() {
		discardReloadResources(rebuiltDB, rebuiltLogger, db, lg, systemLogger, accessLogger)
	}

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
	engine, modules, err := a.buildHTTPWith(rt, lg, systemLogger, accessLogger, factories, injected, registrars)
	if err != nil {
		discard()
		return err
	}

	// Start the new modules before publishing them: if any of them fails, the
	// old routes and modules are still the live ones.
	// 先启动新模块再发布：任一模块启动失败时，仍在服务的是旧路由和旧模块。
	started := make([]support.Module, 0, len(modules))
	for _, m := range modules {
		// Injected instances were started at startup and keep running.
		// 注入的实例在启动时已经启动过，继续保持运行。
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
	// 所有替换资源都已就绪，因此在一个临界区内一次性发布；读取方不会看到只换了一
	// 半的 App。
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
	a.accessLogger = accessLogger
	a.db, a.ownDB = db, ownDB
	a.rt = rt
	a.modules = modules
	a.started = allStarted

	newGen := newGeneration(oldGen.id+1, engine)
	newGen.started = started
	// The binder is composed from the modules of this generation, so the state
	// a request reads is the state built by this reload — and a reload that
	// failed above never reaches this line, leaving the serving generation's
	// binder and state untouched.
	// binder 由本代模块组合，请求读到的就是本次 Reload 构建的状态；上面任何一步
	// 失败都到不了这里，正在服务那一代的 binder 与状态因此不受影响。
	newGen.bind = composeRequestBinders(allStarted)
	newGen.db, newGen.ownDB = db, ownDB
	newGen.logger, newGen.systemLogger, newGen.accessLogger, newGen.ownLogger = lg, systemLogger, accessLogger, ownLogger
	a.gen = newGen
	a.handlerRef.Store(newGen)
	a.mu.Unlock()

	// Requests already in flight on the old generation keep their lease; new
	// requests enter the new one. The old modules and resources are released
	// only after those requests finish (or the drain timeout expires).
	// 已在旧代上处理的请求保留其 lease，新请求进入新代。旧模块和资源要等这些请求
	// 结束（或排空超时）后才释放。
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
// discardReloadResources 只释放本次 Reload 构建的资源，被拒绝的 Reload 因此不会
// 关闭线上数据库或 Sync 线上 logger。
func discardReloadResources(rebuiltDB, rebuiltLogger bool, db *gorm.DB, lg, system, access *zap.Logger) {
	if rebuiltDB && db != nil {
		_ = gofydb.Close(db)
	}
	if rebuiltLogger {
		syncLoggers(lg, system, access)
	}
}

// stopModuleList stops modules in reverse registration order, like cleanup,
// and gives each Stop its own timeout so one hung module cannot block the
// others.
// stopModuleList 按注册的逆序停止模块（与 cleanup 一致），并给每次 Stop 单独的
// 超时，一个卡住的模块不会堵住其他模块。
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
		err := stopModule(stopper, stopCtx)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("gofy: module %q stop: %w", modules[i].Name(), err))
		}
	}
	return errs
}
