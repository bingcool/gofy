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

	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/internal/pidfile"
	"github.com/bingcool/gofy/internal/process"
	"github.com/bingcool/gofy/support"
	gofydb "github.com/bingcool/gofy/support/db"
	"github.com/bingcool/gofy/support/middleware"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/bingcool/gofy/support/otel"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Run starts the HTTP server and blocks until the context is cancelled, the
// listener fails, or a fatal runtime error is reported.
// Run 启动 HTTP 服务器并阻塞，直到 context 被取消、listener 出错或上报了致命
// 运行时错误；返回的错误同时包含关闭过程中的错误。
func (a *App) Run(ctx context.Context) error {
	if err := a.beginStart(false); err != nil {
		return err
	}
	return a.run(ctx)
}

// Handler builds and returns an http.Handler without listening on a port.
// Modules are registered but not started; call Shutdown to release resources.
// Handler 构建并返回 http.Handler，但不监听端口。模块只注册不启动，调用方需
// 自行调用 Shutdown 释放资源；该模式下不会写 pid 文件，也不会注册 Nacos。
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

	lg, system, access, own, err := a.buildLoggers()
	if err != nil {
		return nil, a.abort(err)
	}
	a.setLogger(lg, system, access, own)

	telemetry, err := a.buildTelemetry(runCtx, lg)
	if err != nil {
		return nil, a.abort(err)
	}
	a.telemetry = telemetry

	db, ownDB, err := a.buildDB(runCtx, lg)
	if err != nil {
		return nil, a.abort(err)
	}
	if err := a.instrumentDB(db, ownDB); err != nil {
		return nil, a.abort(err)
	}
	a.db, a.ownDB = db, ownDB

	rt := core.NewRuntime(core.RuntimeConfig{
		Name:           a.opts.name,
		Env:            a.opts.env,
		Logger:         lg,
		DB:             db,
		Version:        a.version,
		Ctx:            runCtx,
		Fail:           func(err error) { cancel(err) },
		TracerProvider: telemetry.TracerProvider(),
		Propagator:     telemetry.Propagator(),
		Tracing:        telemetry.Enabled(),
	})
	a.rt = rt

	engine, modules, err := a.buildHTTP(rt, lg, system, access)
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

	lg, system, access, own, err := a.buildLoggers()
	if err != nil {
		return a.abort(err)
	}
	a.setLogger(lg, system, access, own)

	telemetry, err := a.buildTelemetry(ctx, lg)
	if err != nil {
		return a.abort(err)
	}
	a.telemetry = telemetry

	db, ownDB, err := a.buildDB(ctx, lg)
	if err != nil {
		return a.abort(err)
	}
	if err := a.instrumentDB(db, ownDB); err != nil {
		return a.abort(err)
	}
	a.db, a.ownDB = db, ownDB

	rt := core.NewRuntime(core.RuntimeConfig{
		Name:           a.opts.name,
		Env:            a.opts.env,
		Logger:         lg,
		DB:             db,
		Version:        a.version,
		Ctx:            ctx,
		Fail:           func(err error) { cancel(err) },
		TracerProvider: telemetry.TracerProvider(),
		Propagator:     telemetry.Propagator(),
		Tracing:        telemetry.Enabled(),
	})
	a.rt = rt

	engine, modules, err := a.buildHTTP(rt, lg, system, access)
	if err != nil {
		return a.abort(err)
	}
	a.modules = modules

	pidPath, err := a.resolvePIDPath()
	if err != nil {
		return a.abort(err)
	}
	if existing, duplicate := duplicatePID(pidPath, os.Getpid()); duplicate {
		return a.abort(fmt.Errorf("%w: pid %d (%s)", ErrAlreadyRunning, existing, pidPath))
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

	// The first generation owns the factory-built modules; the injected ones
	// belong to the App and keep running across reloads.
	// 第一代持有工厂构建的模块；注入的模块归 App 所有，跨 Reload 持续运行。
	gen := newGeneration(1, engine)
	gen.started = a.factoryStarted()
	// Bind module-owned state to this generation only now, after every module
	// Start has produced it: requests and the state they read become visible
	// in the same step.
	// 到这里才把模块状态绑进本代（各模块 Start 已完成）：请求与它读到的状态在同
	// 一步变得可见。
	gen.bind = composeRequestBinders(a.started)
	gen.db, gen.ownDB = db, ownDB
	gen.logger, gen.systemLogger, gen.accessLogger, gen.ownLogger = lg, system, access, own
	// The server keeps this wrapper for its whole life; Reload only swaps the
	// generation stored inside it, so the listener is never touched.
	// server 整个生命周期都持有这个包装器；Reload 只替换其中存放的 generation，
	// listener 因此始终不动。
	a.handlerRef = newHandlerSwap(gen, a.admission)
	a.gen = gen
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
	// 只在 listener 已能接受连接后才注册，发现服务的客户端因此不会把流量打到还
	// 没准备好的端口上。
	if err := a.startNacosRegistration(ctx, listener, lg); err != nil {
		return a.abort(err)
	}

	lg.Info("application started",
		zap.String("name", a.opts.name),
		zap.String("env", a.opts.env),
		zap.String("addr", addr),
		zap.String("version", a.version.Version),
		zap.Bool("telemetry", a.telemetry.Enabled()),
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

func (a *App) buildHTTP(rt *core.Runtime, lg, system, access *zap.Logger) (*gin.Engine, []support.Module, error) {
	return a.buildHTTPWith(rt, lg, system, access, a.opts.moduleFactories, a.opts.modules, a.opts.middlewareRegistrars)
}

func (a *App) buildHTTPWith(rt *core.Runtime, lg, system, access *zap.Logger, factories []support.ModuleFactory, injected []support.Module, registrars []MiddlewareRegistrar) (*gin.Engine, []support.Module, error) {
	gin.SetMode(ginMode(a.opts.http.Mode))
	engine := gin.New()
	registry := middleware.NewRegistry()
	register := func(name string, mw support.Middleware) error {
		if err := registry.Register(name, mw); err != nil {
			return fmt.Errorf("gofy: register middleware %q: %w", name, err)
		}
		return nil
	}
	if len(a.opts.trusted) > 0 {
		if err := engine.SetTrustedProxies(a.opts.trusted); err != nil {
			return nil, nil, fmt.Errorf("gofy: trusted proxies: %w", err)
		}
	} else {
		// An empty slice disables gin's default "trust every proxy" behavior.
		// 空切片会关闭 gin 默认的“信任所有代理”行为；否则客户端 IP 可被伪造，
		// 影响访问日志与限流维度。
		if err := engine.SetTrustedProxies([]string{}); err != nil {
			return nil, nil, fmt.Errorf("gofy: disable trusted proxies: %w", err)
		}
	}

	// The middleware install order is an external contract
	// (docs/library-design.md §3.4.6, §7.1): changing it requires updating
	// TestMiddlewareOrderContract in the same change. Key constraints: RequestID
	// stays outermost because logs, spans and propagated headers depend on req_id;
	// Trace follows RequestID so HeaderPropagation and AccessLog can read the OTel
	// trace id; AccessLog wraps Recovery so a panic turned into 500 is still logged
	// with its final status; Recovery sits inside CORS so panic responses still carry
	// CORS headers; BodyLimit must precede every handler that reads the body; App
	// Middleware and module routes sit inside Recovery so business panics are caught,
	// while the framework middleware before it runs no user code and its panics fall
	// back to net/http, so a second Recovery layer here would change what AccessLog
	// records.
	// 中间件安装顺序是对外契约（docs/library-design.md §3.4.6、§7.1），
	// 修改时必须同步 TestMiddlewareOrderContract：
	//
	//   RequestID → Telemetry Trace → HeaderPropagation → SecurityHeaders
	//   → AccessLog → CORS → Recovery → BodyLimit
	//   → App Middleware → healthz/readyz
	//   → Base Router(RateLimit → JWTAuth)
	//   → MiddlewareRegistrars → Module Routes
	//
	// 关键约束：
	//   - RequestID 最外层，后续日志、span、透传头都依赖 req_id；
	//   - Trace 紧跟 RequestID，HeaderPropagation 与 AccessLog 才能读到 OTel
	//     trace id；
	//   - AccessLog 在 Recovery 外层，panic 被转成 500 后访问日志记录的仍是
	//     最终状态码；
	//   - Recovery 在 CORS 内层，panic 响应仍带跨域头；
	//   - BodyLimit 必须在任何读取 body 的 handler 之前；
	//   - App Middleware 与模块路由都在 Recovery 内层，业务 panic 会被捕获；
	//     Recovery 之前的框架中间件不执行用户代码，其 panic 由 net/http 兜底，
	//     这里不再加第二层 Recovery，否则会改变 AccessLog 的记录语义。
	requestID := middleware.RequestID(support.DefaultRequestIDHeader)
	engine.Use(requestID)
	if err := register(middleware.NameRequestID, requestID); err != nil {
		return nil, nil, err
	}
	// The Trace middleware follows RequestID: the span must exist first so
	// HeaderPropagation can fall back to the trace id for X-Trace-Id, and probe
	// requests are not traced.
	// Trace 中间件紧跟 RequestID：先建立 span，后续 HeaderPropagation 才能用
	// trace id 兜底 X-Trace-Id；探针请求不进入 trace。
	if traceMW := a.telemetry.GinMiddleware(otel.SkipPaths("/healthz", "/readyz")); traceMW != nil {
		engine.Use(traceMW)
	}
	if a.opts.headerPropagation {
		// Installed right after RequestID: fall back to req_id or the OTel trace id for
		// X-Trace-Id, and make the propagated headers readable from the context by the
		// middleware and handlers that follow.
		// 紧贴 RequestID 安装：用 req_id 或 OTel trace id 兜底 X-Trace-Id，
		// 并保证后续业务中间件和 handler 都能从 context 读到透传头。
		headerPropagation := middleware.HeaderPropagation(a.opts.trusted)
		engine.Use(headerPropagation)
		if err := register(middleware.NameHeaderPropagation, headerPropagation); err != nil {
			return nil, nil, err
		}
	}
	if a.opts.securityHeaders != nil {
		mw, err := middleware.SecurityHeaders(*a.opts.securityHeaders)
		if err != nil {
			return nil, nil, err
		}
		engine.Use(mw)
		if err := register(middleware.NameSecurityHeaders, mw); err != nil {
			return nil, nil, err
		}
	}
	if a.opts.accessLog {
		// DefaultAccessLogOptions apply by default; a config passed to
		// WithAccessLogOptions is taken literally and is not merged with the defaults.
		// 默认用 DefaultAccessLogOptions；WithAccessLogOptions 传入的配置按字面
		// 生效，不再与默认值合并。
		accessLogOpts := middleware.DefaultAccessLogOptions()
		if a.opts.accessLogOptions != nil {
			accessLogOpts = *a.opts.accessLogOptions
		}
		accessLog := middleware.AccessLog(access, accessLogOpts)
		engine.Use(accessLog)
		if err := register(middleware.NameAccessLog, accessLog); err != nil {
			return nil, nil, err
		}
	}
	if a.opts.cors != nil {
		corsMW, err := middleware.CORS(*a.opts.cors)
		if err != nil {
			return nil, nil, err
		}
		if corsMW != nil {
			engine.Use(corsMW)
			if err := register(middleware.NameCORS, corsMW); err != nil {
				return nil, nil, err
			}
		}
	}
	if a.opts.recovery {
		recovery := middleware.Recovery(system, a.opts.responder)
		engine.Use(recovery)
		if err := register(middleware.NameRecovery, recovery); err != nil {
			return nil, nil, err
		}
	}
	bodyLimit := middleware.BodyLimit(a.opts.maxBody)
	engine.Use(bodyLimit)
	if err := register(middleware.NameBodyLimit, bodyLimit); err != nil {
		return nil, nil, err
	}
	if len(a.opts.middleware) > 0 {
		engine.Use(a.opts.middleware...)
	}
	if a.opts.health {
		registerHealth(engine, rt)
	}
	registerFallbacks(engine, a.opts.responder)

	// The root Router carries the single middleware registry of this process and every
	// module group shares it through the engine; it must be created after the global
	// engine.Use calls so it inherits the global chain.
	// 根 Router 携带本进程唯一的中间件注册表，所有模块分组都通过
	// engine 共享它；必须在全局 engine.Use 之后创建，才能继承全局链。
	base := support.NewRouter(engine, support.RouterConfig{
		Prefix:     a.opts.apiPrefix,
		Responder:  a.opts.responder,
		Validator:  support.NewValidator(),
		Middleware: registry,
	})

	// Rate limiting and JWT apply to module routes only, so probes and unknown
	// paths keep their plain behavior (200 and 404).
	// 限流与 JWT 只作用于模块路由，探针和未知路径因此保持原样（200 与 404）。
	if a.opts.rateLimit != nil {
		mw, err := middleware.RateLimit(*a.opts.rateLimit, a.opts.responder)
		if err != nil {
			return nil, nil, err
		}
		base.Use(mw)
		if err := register(middleware.NameRateLimit, mw); err != nil {
			return nil, nil, err
		}
	}
	if a.opts.jwt != nil {
		mw, err := middleware.JWTAuth(*a.opts.jwt, a.opts.responder)
		if err != nil {
			return nil, nil, err
		}
		base.Use(mw)
		if err := register(middleware.NameJWTAuth, mw); err != nil {
			return nil, nil, err
		}
	}
	// Application middleware registers after the built-in entries; registering the
	// same alias twice fails at startup.
	// 应用层中间件在框架内建项之后注册；同别名重复注册会在启动期直接失败。
	for i, registrar := range registrars {
		if err := registrar(MiddlewareRegistration{
			Runtime:   rt,
			Registry:  registry,
			Responder: a.opts.responder,
		}); err != nil {
			return nil, nil, fmt.Errorf("gofy: middleware registrar %d: %w", i, err)
		}
	}
	modules, err := a.buildModulesWith(rt, factories, injected)
	if err != nil {
		return nil, nil, err
	}
	if err := a.registerModules(base, modules); err != nil {
		return nil, nil, err
	}
	return engine, modules, nil
}

func (a *App) buildModules(rt *core.Runtime) ([]support.Module, error) {
	return a.buildModulesWith(rt, a.opts.moduleFactories, a.opts.modules)
}

func (a *App) buildModulesWith(rt *core.Runtime, factories []support.ModuleFactory, injected []support.Module) ([]support.Module, error) {
	modules := make([]support.Module, 0, len(factories)+len(injected))
	seen := make(map[string]struct{}, cap(modules))

	add := func(m support.Module) error {
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

func callFactory(factory support.ModuleFactory, rt *core.Runtime) (m support.Module, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic: %v", rec)
		}
	}()
	return factory(rt)
}

func (a *App) registerModules(base *support.Router, modules []support.Module) error {
	used := make(map[string]string, len(modules))
	for _, m := range modules {
		prefix := "/" + m.Name()
		if p, ok := m.(support.Prefixer); ok {
			prefix = p.Prefix()
		}
		prefix = support.NormalizePrefix(prefix)
		full := support.NormalizePrefix(a.opts.apiPrefix + prefix)
		// An empty prefix means the module mounts on the root Router and decides its
		// real route prefix itself (usually the full deployment prefix of the contract
		// group); the framework cannot detect conflicts from the mount point alone,
		// since several modules mounting /api/v1/order or /api/v1/product is normal
		// usage. Truly duplicate routes are reported by gin at registration time, and
		// registerModule turns that panic into a startup error. Non-empty mount points
		// are still deduplicated by prefix.
		// 空前缀表示模块挂在根 Router 上，真实路由前缀由模块自己决定（通常
		// 来自契约分组的完整部署前缀），框架只凭挂载点无从判断冲突：多个模块
		// 各挂 /api/v1/order、/api/v1/product 是正常用法。真正重复的路由由 gin
		// 在注册阶段报错，registerModule 会把 panic 转成启动错误。非空挂载点
		// 仍按前缀去重。
		if full != "" {
			if other, ok := used[full]; ok {
				return fmt.Errorf("gofy: route prefix %q is used by both %q and %q", full, other, m.Name())
			}
			used[full] = m.Name()
		}
		if err := registerModule(m, base.ModuleGroup(prefix)); err != nil {
			return err
		}
	}
	return nil
}

func registerModule(m support.Module, r *support.Router) (err error) {
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
		starter, ok := m.(support.Starter)
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

// factoryStarted returns the started modules Reload owns, excluding instances
// injected with WithModules: those belong to the App and keep running across
// reloads.
// factoryStarted 返回 Reload 需要负责的已启动模块，排除经 WithModules 注入的
// 实例：它们归 App 所有，跨 Reload 持续运行。
func (a *App) factoryStarted() []support.Module {
	if len(a.opts.modules) == 0 {
		return a.started
	}
	injectedNames := make(map[string]struct{}, len(a.opts.modules))
	for _, m := range a.opts.modules {
		injectedNames[m.Name()] = struct{}{}
	}
	started := make([]support.Module, 0, len(a.started))
	for _, m := range a.started {
		if _, ok := injectedNames[m.Name()]; ok {
			continue
		}
		started = append(started, m)
	}
	return started
}

// startNacosRegistration publishes this instance once the listener is up. A
// registration failure aborts startup: running without being discoverable is
// worse than failing fast.
// startNacosRegistration 在 listener 就绪后发布本实例。注册失败会中止启动：
// 带着不可发现的状态继续跑，比快速失败更糟。
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

	// The watcher module starts before this point, so a config change can close
	// the gate while registration is in flight. Checking afterwards — rather
	// than only before — covers both orders: whatever was just registered is
	// withdrawn again, and the process never advertises an instance that already
	// answers 503. Failing to withdraw is fatal here: starting anyway would
	// leave exactly the inconsistent state this check exists to prevent.
	// watcher 模块在此前就已启动，因此注册进行中配置变更可能已经关闭闸门。注册之后
	// 再检查（而不是只在之前检查）覆盖了两种顺序：刚注册的实例会被撤销，进程绝不会
	// 对外通告一个已经在返回 503 的实例。撤销失败在这里是致命错误：继续启动恰好会留下
	// 这个检查要消除的不一致状态。
	if !a.admission.isOpen() {
		lg.Info("drain requested during startup, withdrawing the registration")
		if err := registrar.Stop(ctx); err != nil {
			return fmt.Errorf("gofy: nacos service deregistration during startup: %w", err)
		}
	}
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

// duplicatePID reports a live pid recorded in pidPath that belongs to another
// process. A pid equal to self means this process re-executed itself (the
// restart strategy): the previous image is gone by definition, so the file is
// simply refreshed rather than treated as a second instance.
// duplicatePID 返回 pidPath 中记录的、属于其它进程且仍然存活的 pid。pid 等于 self
// 表示本进程刚 re-exec 自重启（restart 策略）：旧映像按定义已不存在，只需刷新
// 文件，不应判为第二个实例。
func duplicatePID(pidPath string, self int) (int, bool) {
	if pidPath == "" {
		return 0, false
	}
	existing, err := pidfile.Read(pidPath)
	if err != nil || existing == self {
		return 0, false
	}
	return existing, process.Alive(existing)
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

// registerFallbacks renders unmatched paths and unsupported methods with the
// same envelope as handlers. Without NoRoute, gin returns a plain-text 404.
// registerFallbacks 用与 handler 相同的包络渲染未匹配路径和不支持的方法；没有
// NoRoute 时 gin 会返回纯文本 404，破坏响应格式的统一。
func registerFallbacks(engine *gin.Engine, responder support.Responder) {
	engine.HandleMethodNotAllowed = true
	engine.NoRoute(func(c *gin.Context) {
		responder.Error(c, apperr.NotFound("route not found"))
	})
	engine.NoMethod(func(c *gin.Context) {
		responder.Error(c, apperr.MethodNotAllowed("method not allowed"))
	})
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

// StopAccepting refuses new HTTP requests and removes this instance from Nacos
// naming without stopping the server. It is the quiesce step used by the nacos
// restart and exit strategies: the listener stays open so Serve keeps running,
// requests already in flight keep their generation lease, and a readiness probe
// sees 503 while the watcher waits out the grace period before exec/terminate.
//
// It is idempotent and safe to call from any goroutine, including the watcher
// that will later exec the process. It deliberately does not call Shutdown:
// doing so would stop the modules, join that very watcher and take main out of
// the process before the strategy action runs.
// StopAccepting 拒绝新的 HTTP 请求并从 Nacos 注销本实例，但不停止 server。它是
// nacos restart / exit 策略的摘流步骤：listener 保持打开，Serve 继续运行，在途
// 请求仍持有 generation lease，健康检查在 watcher 等待 grace period 期间读到
// 503，然后才 exec / terminate。
//
// 该方法幂等，可在任意 goroutine（包括随后要 exec 的 watcher）调用。它刻意不
// 调用 Shutdown：那会停掉模块、等待 watcher 自身，并在策略动作执行前就让 main
// 退出。
//
// registrar 的引用会保留：动作可能失败，届时 ResumeAccepting 需要它重新注册。
// 重复 Stop 是 no-op，因此关闭流程里的第二次 Stop 不受影响。
func (a *App) StopAccepting(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !a.admission.close() {
		// Already draining: do not deregister twice or log twice.
		// 已经在摘流：不重复注销，也不重复记日志。
		return nil
	}
	a.Logger().Info("application stopped accepting HTTP requests")

	a.mu.Lock()
	registrar := a.registrar
	a.mu.Unlock()
	if registrar == nil {
		return nil
	}
	if err := registrar.Stop(ctx); err != nil {
		return fmt.Errorf("gofy: deregister before restart: %w", err)
	}
	return nil
}

// ResumeAccepting reopens the gate after a restart or exit turned out to be
// impossible. Without it a process whose exec or signal failed would stay alive,
// deregistered and answering 503 forever — a zombie only a liveness probe keyed
// on /healthz would ever restart.
//
// It is the counterpart of StopAccepting, not a general lifecycle method: it
// assumes the app is running and only the drain gate was closed.
// ResumeAccepting 在 restart / exit 未能执行时重新打开闸门。没有它，exec 或发信号
// 失败的进程会一直活着、已注销、永远 503 —— 只有把 liveness 指到 /healthz 的探针
// 才会把它重启。
//
// 它是 StopAccepting 的逆操作，不是通用生命周期方法：前提是 App 仍在运行，只是
// 闸门被关闭了。
func (a *App) ResumeAccepting(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !a.admission.resume() {
		// Never drained: nothing to undo.
		// 从未摘流，无需恢复。
		return nil
	}
	a.Logger().Info("application resumed accepting HTTP requests")

	a.mu.Lock()
	registrar := a.registrar
	a.mu.Unlock()
	if registrar == nil {
		return nil
	}
	if err := registrar.Start(ctx); err != nil {
		return fmt.Errorf("gofy: reregister after failed process action: %w", err)
	}
	return nil
}

// Shutdown triggers a graceful stop. It is idempotent and safe to call
// concurrently. Calling it before the app starts is a no-op.
// Shutdown 触发优雅停止：可重复调用，也可并发调用；App 尚未启动时调用是 no-op。
// 返回的是清理阶段累积的错误，且在停止完成后重复调用会得到同一结果。
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
// cleanup 只执行一次，释放 App 持有的全部资源；重复调用返回首次的结果。
func (a *App) cleanup() error {
	a.cleanupOnce.Do(func() {
		// Wait for an in-flight Reload: it may still be swapping modules and
		// resources, and cleanup must see the final state.
		// 等待在途的 Reload 结束：它可能还在替换模块与资源，清理必须看到最终状态。
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
		// 先注销再关闭 server：发现服务的客户端会停止把新请求路由过来，在途请求
		// 仍能正常结束。
		a.mu.Lock()
		registrar := a.registrar
		a.registrar = nil
		a.mu.Unlock()
		if registrar != nil {
			if err := registrar.Stop(ctx); err != nil {
				lg.Warn("nacos service deregistration failed", zap.Error(err))
			}
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
			stopper, ok := m.(support.Stopper)
			if !ok {
				continue
			}
			if err := stopper.Stop(ctx); err != nil {
				lg.Error("module stop failed", zap.String("module", m.Name()), zap.Error(err))
				errs = append(errs, fmt.Errorf("gofy: module %q stop: %w", m.Name(), err))
			}
		}

		if a.ownDB && a.db != nil {
			if err := gofydb.Close(a.db); err != nil {
				lg.Error("close database failed", zap.Error(err))
				errs = append(errs, err)
			}
		}

		// Spans produced by module shutdown and database close are still in the buffer,
		// so flush them all here. An export failure is only logged: an unavailable
		// collector must not be escalated into a shutdown error.
		// 模块停机和数据库关闭产生的 span 也在缓冲区内，最后统一 flush。
		// 导出失败只记录日志，不把采集器不可用升级成停机错误。
		_ = a.telemetry.Shutdown(ctx)

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
			syncLoggers(a.logger, a.systemLogger, a.accessLogger)
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
