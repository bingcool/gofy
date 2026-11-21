// Package gofy is a small application framework built on top of gin.
// Applications create an App with New, then call Run, Execute or Handler.
// 包 gofy 是基于 gin 的轻量应用框架：先用 New 创建 App，再调用 Run、Execute
// 或 Handler 启动，同一个 App 只能启动一次。
package gofy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bingcool/gofy/command"
	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/internal/version"
	"github.com/bingcool/gofy/logger"
	"github.com/bingcool/gofy/script"
	"github.com/bingcool/gofy/support"
	gofydb "github.com/bingcool/gofy/support/db"
	"github.com/bingcool/gofy/support/diagnostics"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/bingcool/gofy/support/otel"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type appState int

const (
	stateNew appState = iota
	stateBuilding
	stateRunning
	stateStopped
	stateFailed
)

// App is a single application run: configuration, logger, DB, HTTP server,
// modules, scripts and commands. The zero value is not usable.
// App 代表一次应用运行所需的配置、logger、数据库、HTTP 服务、模块、脚本与
// 命令。零值不可用，必须经 New 构造后才能启动。
type App struct {
	opts    options
	version core.VersionInfo

	logger       *zap.Logger
	systemLogger *zap.Logger
	accessLogger *zap.Logger
	sqlLogger    *zap.Logger
	ownLogger    bool

	mu          sync.Mutex
	state       appState
	handlerMode bool

	stopCh    chan struct{}
	stopOnce  sync.Once
	done      chan struct{}
	ready     chan struct{}
	readyOnce sync.Once

	// notifier reports the startup outcome to the parent that detached this
	// process. It is nil for every process that was not started as a daemon
	// child, and a nil notifier is a no-op.
	// notifier 向脱离出本进程的父进程报告启动结果。非后台子进程为 nil，而 nil 通知器
	// 什么都不做。
	notifier *startupNotifier

	// admission is the HTTP door every request passes before it may enter a
	// generation. StopAccepting closes it before a restart or exit, so new
	// requests are refused with 503 while in-flight ones keep running;
	// ResumeAccepting reopens it when that action failed. The gate lives on App
	// rather than on the handlerSwap because a watcher may close it before the
	// server exists.
	// admission 是每个 HTTP 请求进入 generation 之前都要穿过的入口。
	// restart / exit 前 StopAccepting 关闭它：新请求返回 503，在途请求继续跑完；
	// 动作失败时 ResumeAccepting 重新打开。闸门放在 App 而不是 handlerSwap 上，
	// 是因为 watcher 可能在 server 建好之前就关闭它。
	admission *admissionGate

	cleanupOnce sync.Once
	cleanupErr  error

	// abandonedWorkers lists the worker groups whose instances were still
	// running when the drain budget ran out. cleanup reads it to decide whether
	// the resources those instances use may be torn down. It is written by the
	// worker host before cleanup and never mutated afterwards.
	// abandonedWorkers 记录 drain 预算耗尽时仍有实例在跑的分组。cleanup 据此决定
	// 是否可以拆除这些实例正在使用的资源。它由 worker 宿主在 cleanup 之前写入，
	// 之后不再修改。
	abandonedWorkers []string

	// reloadMu serializes Reload with cleanup and with other Reload calls, so a
	// runtime swap never races a shutdown or a second swap.
	// reloadMu 让 Reload 与清理、以及多个 Reload 之间串行，避免运行时换代
	// 与关闭流程或另一次换代并发。
	reloadMu sync.Mutex

	cancel context.CancelCauseFunc
	rt     *core.Runtime
	db     *gorm.DB
	ownDB  bool
	// telemetry is built at startup and reused by every reload. It is nil when
	// WithTelemetry was not used or tracing is disabled.
	// telemetry 在启动时构建，并被每次 Reload 复用；未使用 WithTelemetry 或
	// tracing 关闭时为 nil。
	telemetry *otel.Provider

	// metrics is built at startup when WithDiagnostics enabled the scrape
	// endpoint. A nil value is a disabled pipeline: GinMiddleware returns nil and
	// Shutdown is a no-op, so the HTTP path needs no nil checks.
	// metrics 在 WithDiagnostics 打开抓取端点时于启动期构建。nil 表示管道禁用：
	// GinMiddleware 返回 nil、Shutdown 是 no-op，HTTP 路径因此不需要判空。
	metrics *otel.Metrics

	// diagnostics is the operational listener (metrics + pprof). It is nil when
	// WithDiagnostics was not used or the listener is disabled.
	// diagnostics 是运维监听（指标 + pprof）；未使用 WithDiagnostics 或监听关闭时为 nil。
	diagnostics *diagnostics.Server
	srv         *http.Server
	listener    net.Listener
	serving     bool
	// handlerRef is the http.Handler given to the server. run installs a
	// handlerSwap here so Reload can replace the route table without rebinding
	// the listener; Handler mode has no server and leaves it nil. gen is the
	// generation currently published inside it.
	// handlerRef 是交给 HTTP server 的 http.Handler：run 在其中安装
	// handlerSwap，使 Reload 无需重新绑定 listener 即可替换路由表；Handler
	// 模式没有 server，该字段保持 nil。gen 是其中当前发布的 generation。
	handlerRef *handlerSwap
	gen        *generation
	modules    []support.Module
	started    []support.Module

	// registrar publishes this instance in Nacos naming; nil when
	// registration is disabled or the App runs in Handler mode.
	// registrar 把本实例发布到 Nacos 命名服务；注册被关闭或 App 运行在
	// Handler 模式时为 nil。
	registrar *nacos.Registrar

	pidPath       string
	pidTicker     *time.Ticker
	pidTickerDone chan struct{}
	// pidRefreshExited closes when the refresh goroutine has returned. cleanup
	// waits on it before removing the file, otherwise a rename already in
	// flight recreates the file after deletion.
	// pidRefreshExited 在刷新 goroutine 返回时关闭。cleanup 先等它再删文件，
	// 否则一次已在途的 rename 会在删除之后把文件重建出来。
	pidRefreshExited chan struct{}
}

// New validates options and builds an App. It reads no configuration file,
// opens no connection, creates no directory, writes no pid file, listens on no
// port and registers no signal handler.
// New 校验选项并构造 App：不读配置文件、不建连接、不建目录、不写 pid 文件、
// 不监听端口，也不注册信号处理，因此可在启动前安全调用。
func New(opts ...Option) (*App, error) {
	o := defaultOptions()
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&o); err != nil {
			return nil, err
		}
	}
	if err := o.validate(); err != nil {
		return nil, err
	}
	if o.alwaysOK && !o.responderSet {
		o.responder = support.NewResponder(support.ResponderConfig{AlwaysOK: true})
	}

	a := &App{
		opts:      o,
		admission: newAdmissionGate(),
		stopCh:    make(chan struct{}),
		done:      make(chan struct{}),
		ready:     make(chan struct{}),
		notifier:  notifierFromEnv(),
	}
	if o.hasVer {
		a.version = o.version
	} else {
		a.version = version.Read(Version)
	}
	a.logger = o.logger
	if a.logger == nil {
		a.logger = logger.Nop()
	}
	// Without a separate system_error_outputs setting, framework errors reuse the
	// main logger so panics keep landing in error_outputs. The same fallback
	// applies to access_outputs: without it, access entries stay in the main logger.
	// 未单独配置 system_error_outputs 时，框架错误沿用主 logger，保持
	// panic 仍然写入 error_outputs 的既有行为。access_outputs 同理：未配置时
	// 访问日志继续写主 logger。
	a.systemLogger = a.logger
	a.accessLogger = a.logger
	a.sqlLogger = a.logger
	return a, nil
}

func (o *options) validate() error {
	if !namePattern.MatchString(o.name) {
		return fmt.Errorf("gofy: invalid app name %q", o.name)
	}
	if strings.TrimSpace(o.env) == "" {
		return fmt.Errorf("gofy: env must not be empty")
	}
	if o.dbConfig != nil && o.db != nil {
		return fmt.Errorf("gofy: WithDatabase and WithDB are mutually exclusive")
	}
	if err := script.Validate(o.scripts); err != nil {
		return err
	}
	if err := command.Validate(o.commands); err != nil {
		return err
	}
	if err := o.validateWorkerConcurrency(); err != nil {
		return err
	}
	if o.shutdownTimeout <= 0 {
		return fmt.Errorf("gofy: shutdown timeout must be positive")
	}
	if o.workerDrain <= 0 {
		return fmt.Errorf("gofy: worker drain timeout must be positive")
	}
	if o.maxBody <= 0 {
		return fmt.Errorf("gofy: max body bytes must be positive")
	}
	if o.responder == nil {
		return fmt.Errorf("gofy: responder must not be nil")
	}
	return nil
}

// validateWorkerConcurrency rejects a key that addresses no registered worker.
// With several groups a stale key is easy to miss, and its only symptom would be
// that worker quietly running a single instance.
// validateWorkerConcurrency 拒绝没有对应 worker 的键。有多个分组时过期的键很容易
// 被忽略，而它唯一的表现就是那个 worker 悄悄只跑一个实例。
func (o *options) validateWorkerConcurrency() error {
	if len(o.workerConcurrency) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(o.workerGroups))
	for _, g := range o.workerGroups {
		for _, w := range g.Workers() {
			known[g.Name()+"/"+w.Name()] = struct{}{}
		}
	}
	keys := make([]string, 0, len(o.workerConcurrency))
	for key := range o.workerConcurrency {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := known[key]; !ok {
			return fmt.Errorf("gofy: worker concurrency key %q matches no registered worker", key)
		}
	}
	return nil
}

// Logger returns the application logger. It is never nil.
// Logger 返回应用 logger，永不为 nil。
func (a *App) Logger() *zap.Logger {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.logger == nil {
		a.logger = logger.Nop()
	}
	return a.logger
}

// Version returns build information. Its Version field is the framework version
// (the Version constant) unless the application replaced it with
// WithVersionInfo; commit and build time come from the build, when available.
// Version 返回构建信息。其中 Version 字段是框架版本（Version 常量），除非应用用
// WithVersionInfo 整体替换；commit 与构建时间在可用时取自构建信息。
func (a *App) Version() VersionInfo { return a.version }

// Name returns the application name.
// Name 返回应用名。
func (a *App) Name() string { return a.opts.name }

func (a *App) setState(s appState) {
	a.mu.Lock()
	a.state = s
	a.mu.Unlock()
}

func (a *App) getState() appState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

func (a *App) setLogger(lg loggers) {
	a.mu.Lock()
	a.logger = lg.app
	a.systemLogger = lg.system
	a.accessLogger = lg.access
	a.sqlLogger = lg.sql
	a.ownLogger = lg.own
	a.mu.Unlock()
}

func (a *App) markReady() {
	a.readyOnce.Do(func() { close(a.ready) })
}

// beginStart enforces the single-shot contract of Run/Execute(start)/Handler.
// beginStart 落实 Run/Execute(start)/Handler 只能启动一次的约定，重复调用返
// 回 ErrAlreadyStarted。
func (a *App) beginStart(handler bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != stateNew {
		return ErrAlreadyStarted
	}
	a.state = stateBuilding
	a.handlerMode = handler
	return nil
}

// resolveWorkDir validates and returns the base directory for relative paths.
// resolveWorkDir 校验并返回相对路径的基准目录：未配置时取当前工作目录，
// 已配置时要求该目录在启动前就存在。
func (a *App) resolveWorkDir() (string, error) {
	dir := a.opts.workDir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("gofy: get working directory: %w", err)
		}
		return wd, nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("gofy: work dir %s: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("gofy: work dir %s is not a directory", dir)
	}
	return dir, nil
}

// buildLoggers builds the application logger, the logger for framework-level
// failures and the HTTP access logger. The system logger falls back to the
// application logger when system_error_outputs is empty; the access logger does
// the same when access_outputs is empty. Later failures sync what was already
// built, so an aborted startup leaves no buffered entries behind.
// buildLoggers 构建应用 logger、框架级故障 logger 与 HTTP 访问日志 logger。
// system_error_outputs 为空时系统 logger 回退到应用 logger，access_outputs 为空
// 时访问 logger 同样回退；后续步骤失败会先 Sync 已建好的 logger，启动中止时不会
// 留下未刷盘的缓冲条目。
// loggers is the set of channels the framework builds at startup. It is a
// struct rather than five return values so adding a channel does not reshuffle
// every call site.
// loggers 是框架启动时构建的一组日志通道。用结构体而不是五个返回值，新增通道时不必
// 改动每个调用点。
type loggers struct {
	app    *zap.Logger
	system *zap.Logger
	access *zap.Logger
	sql    *zap.Logger
	own    bool
}

func (a *App) buildLoggers() (loggers, error) {
	if a.opts.logger != nil {
		return loggers{app: a.opts.logger, system: a.opts.logger, access: a.opts.logger, sql: a.opts.logger}, nil
	}
	lg, err := logger.New(a.opts.logConfig)
	if err != nil {
		return loggers{}, fmt.Errorf("gofy: build logger: %w", err)
	}
	system, err := logger.NewSystem(a.opts.logConfig)
	if err != nil {
		syncLoggers(lg)
		return loggers{}, fmt.Errorf("gofy: build system logger: %w", err)
	}
	if len(a.opts.logConfig.SystemErrorOutputs) == 0 {
		system = lg
	}
	access, err := logger.NewAccess(a.opts.logConfig)
	if err != nil {
		syncLoggers(lg, system)
		return loggers{}, fmt.Errorf("gofy: build access logger: %w", err)
	}
	if len(a.opts.logConfig.AccessOutputs) == 0 {
		access = lg
	}
	sqlLog, err := logger.NewSQL(a.opts.logConfig)
	if err != nil {
		syncLoggers(lg, system, access)
		return loggers{}, fmt.Errorf("gofy: build sql logger: %w", err)
	}
	if len(a.opts.logConfig.SQLOutputs) == 0 {
		sqlLog = lg
	}
	return loggers{app: lg, system: system, access: access, sql: sqlLog, own: true}, nil
}

// syncLoggers flushes every distinct logger exactly once, so fallback aliases
// (system or access reusing the application logger) are not synced twice.
// syncLoggers 对每个不同 logger 只刷盘一次；系统或访问 logger 回用到应用 logger
// 的别名不会被重复 Sync。
func syncLoggers(loggers ...*zap.Logger) {
	seen := make(map[*zap.Logger]struct{}, len(loggers))
	for _, l := range loggers {
		if l == nil {
			continue
		}
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		_ = logger.Sync(l)
	}
}

func (a *App) buildDB(ctx context.Context, logger *zap.Logger) (*gorm.DB, bool, error) {
	if a.opts.db != nil {
		return a.opts.db, false, nil
	}
	if a.opts.dbConfig == nil {
		return nil, false, nil
	}
	db, err := gofydb.New(ctx, *a.opts.dbConfig, logger, a.databaseOptions(a.sqlLogger)...)
	if err != nil {
		return nil, false, fmt.Errorf("gofy: connect database: %w", err)
	}
	return db, true, nil
}

// databaseOptions are the options every pool the App opens must carry.
//
// Startup and Reload both go through here, because they used to differ: Reload
// built its pool with neither option, so a reloaded process lost SQL masking,
// sampling and the rate limits, and its SQL entries fell back to the
// application logger. Nothing failed — the process simply behaved differently
// after a config change, which is the hardest kind of difference to attribute.
// databaseOptions 是 App 打开的每个连接池都必须带的选项。
//
// 启动与 Reload 都经由这里，因为它们曾经不一致：Reload 建池时两个选项都没带，重载后的
// 进程因此失去 SQL 脱敏、采样与限流，SQL 条目回落到应用 logger。没有任何报错——进程只是
// 在配置变更之后行为不同，而这是最难归因的一类差异。
func (a *App) databaseOptions(sqlLogger *zap.Logger) []gofydb.Option {
	return []gofydb.Option{
		gofydb.WithSQLPolicy(a.opts.sqlPolicy),
		gofydb.WithSQLLogger(sqlLogger),
	}
}

// buildTelemetry builds the tracing pipeline. A nil telemetry config leaves
// tracing off and returns a nil provider.
// buildTelemetry 构建 tracing 管道；telemetry 配置为 nil 时关闭 tracing 并返回
// nil provider，服务名、版本和环境默认取应用名、构建版本与环境标记。
func (a *App) buildTelemetry(ctx context.Context, logger *zap.Logger) (*otel.Provider, error) {
	if a.opts.telemetry == nil {
		return nil, nil
	}
	provider, err := otel.New(ctx, *a.opts.telemetry,
		otel.WithLogger(logger),
		otel.WithServiceName(a.opts.name),
		otel.WithServiceVersion(a.version.Version),
		otel.WithEnvironment(a.opts.env),
	)
	if err != nil {
		return nil, fmt.Errorf("gofy: telemetry: %w", err)
	}
	return provider, nil
}

// buildMetrics builds the Prometheus pipeline when the diagnostics listener has
// metrics enabled. Metrics are independent of tracing: an application that
// cannot run a collector still needs a scrape endpoint, so this does not consult
// the telemetry config.
// buildMetrics 在诊断监听启用指标时构建 Prometheus 管道。指标与追踪相互独立：
// 跑不起 collector 的应用仍然需要抓取端点，因此这里不读取 telemetry 配置。
func (a *App) buildMetrics(logger *zap.Logger) (*otel.Metrics, error) {
	if a.opts.diagnostics == nil || !a.opts.diagnostics.Enabled || a.opts.diagnostics.DisableMetrics {
		return nil, nil
	}
	metrics, err := otel.NewMetrics(
		otel.MetricsConfig{Enabled: true, Path: a.opts.diagnostics.MetricsPath},
		otel.WithLogger(logger),
		otel.WithServiceName(a.opts.name),
		otel.WithServiceVersion(a.version.Version),
		otel.WithEnvironment(a.opts.env),
	)
	if err != nil {
		return nil, fmt.Errorf("gofy: metrics: %w", err)
	}
	return metrics, nil
}

// startDiagnostics binds the operational listener. A bind failure is fatal: an
// operator who configured metrics expects the endpoint to exist, and a silently
// missing /metrics is indistinguishable from a service that exports nothing.
// startDiagnostics 绑定运维监听。绑定失败是致命的：配置了指标的运维期望端点存在，
// 而静默缺失的 /metrics 与一个什么都不导出的服务无法区分。
func (a *App) startDiagnostics(logger *zap.Logger) error {
	if a.opts.diagnostics == nil || !a.opts.diagnostics.Enabled {
		return nil
	}
	srv, err := diagnostics.New(*a.opts.diagnostics, diagnostics.WithLogger(logger))
	if err != nil {
		return fmt.Errorf("gofy: diagnostics: %w", err)
	}
	endpoints := diagnostics.Endpoints{}
	if a.metrics != nil {
		endpoints.Metrics = a.metrics.Handler()
		endpoints.MetricsPath = a.metrics.Path()
	}
	if err := srv.Start(endpoints); err != nil {
		return fmt.Errorf("gofy: diagnostics: %w", err)
	}
	a.diagnostics = srv
	return nil
}

// instrumentDB adds GORM spans to the pool the App owns. A connection injected
// with WithDB is left untouched, so sharing it does not silently add callbacks.
// instrumentDB 只给 App 自持的连接池添加 GORM span；经 WithDB 注入的连接保持
// 原样，避免共享同一个连接时被悄悄挂上回调。
func (a *App) instrumentDB(db *gorm.DB, own bool) error {
	if !own || db == nil {
		return nil
	}
	if err := a.telemetry.InstrumentGORM(db); err != nil {
		return fmt.Errorf("gofy: instrument database: %w", err)
	}
	return nil
}
