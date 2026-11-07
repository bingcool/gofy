// Package gofy is a small application framework built on top of gin.
// Applications create an App with New, then call Run, Execute or Handler.
package gofy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bingcool/gofy/command"
	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/internal/version"
	"github.com/bingcool/gofy/logging"
	"github.com/bingcool/gofy/script"
	"github.com/bingcool/gofy/support"
	gofydb "github.com/bingcool/gofy/support/db"
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
type App struct {
	opts    options
	version core.VersionInfo

	logger       *zap.Logger
	systemLogger *zap.Logger
	ownLogger    bool

	mu          sync.Mutex
	state       appState
	handlerMode bool

	stopCh    chan struct{}
	stopOnce  sync.Once
	done      chan struct{}
	ready     chan struct{}
	readyOnce sync.Once

	cleanupOnce sync.Once
	cleanupErr  error

	// reloadMu serializes Reload with cleanup and with other Reload calls, so a
	// runtime swap never races a shutdown or a second swap.
	reloadMu sync.Mutex

	cancel context.CancelCauseFunc
	rt     *core.Runtime
	db     *gorm.DB
	ownDB  bool
	// telemetry is built at startup and reused by every reload. It is nil when
	// WithTelemetry was not used or tracing is disabled.
	telemetry *otel.Provider
	srv       *http.Server
	listener  net.Listener
	serving   bool
	// handlerRef is the http.Handler given to the server. run installs a
	// handlerSwap here so Reload can replace the route table without rebinding
	// the listener; Handler mode has no server and leaves it nil.
	handlerRef *handlerSwap
	modules    []support.Module
	started    []support.Module

	// registrar publishes this instance in Nacos naming; nil when
	// registration is disabled or the App runs in Handler mode.
	registrar *nacos.Registrar

	pidPath       string
	pidTicker     *time.Ticker
	pidTickerDone chan struct{}
}

// New validates options and builds an App. It reads no configuration file,
// opens no connection, creates no directory, writes no pid file, listens on no
// port and registers no signal handler.
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
		opts:   o,
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
		ready:  make(chan struct{}),
	}
	if o.hasVer {
		a.version = o.version
	} else {
		a.version = version.Read()
	}
	a.logger = o.logger
	if a.logger == nil {
		a.logger = logging.Nop()
	}
	// 未单独配置 system_error_outputs 时，框架错误沿用主 logger，保持
	// panic 仍然写入 error_outputs 的既有行为。
	a.systemLogger = a.logger
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
	if o.shutdownTimeout <= 0 {
		return fmt.Errorf("gofy: shutdown timeout must be positive")
	}
	if o.maxBody <= 0 {
		return fmt.Errorf("gofy: max body bytes must be positive")
	}
	if o.responder == nil {
		return fmt.Errorf("gofy: responder must not be nil")
	}
	return nil
}

// Logger returns the application logger. It is never nil.
func (a *App) Logger() *zap.Logger {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.logger == nil {
		a.logger = logging.Nop()
	}
	return a.logger
}

// Version returns build information.
func (a *App) Version() VersionInfo { return a.version }

// Name returns the application name.
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

func (a *App) setLogger(l, system *zap.Logger, own bool) {
	a.mu.Lock()
	a.logger = l
	a.systemLogger = system
	a.ownLogger = own
	a.mu.Unlock()
}

func (a *App) markReady() {
	a.readyOnce.Do(func() { close(a.ready) })
}

// beginStart enforces the single-shot contract of Run/Execute(start)/Handler.
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

// buildLoggers builds the application logger and the logger for framework-level
// failures. The system logger falls back to the application logger when
// system_error_outputs is empty, so panics keep reaching error_outputs.
func (a *App) buildLoggers() (lg, system *zap.Logger, own bool, err error) {
	if a.opts.logger != nil {
		return a.opts.logger, a.opts.logger, false, nil
	}
	lg, err = logging.New(a.opts.logConfig)
	if err != nil {
		return nil, nil, false, fmt.Errorf("gofy: build logger: %w", err)
	}
	system, err = logging.NewSystem(a.opts.logConfig)
	if err != nil {
		_ = logging.Sync(lg)
		return nil, nil, false, fmt.Errorf("gofy: build system logger: %w", err)
	}
	if len(a.opts.logConfig.SystemErrorOutputs) == 0 {
		system = lg
	}
	return lg, system, true, nil
}

func (a *App) buildDB(ctx context.Context, logger *zap.Logger) (*gorm.DB, bool, error) {
	if a.opts.db != nil {
		return a.opts.db, false, nil
	}
	if a.opts.dbConfig == nil {
		return nil, false, nil
	}
	db, err := gofydb.New(ctx, *a.opts.dbConfig, logger)
	if err != nil {
		return nil, false, fmt.Errorf("gofy: connect database: %w", err)
	}
	return db, true, nil
}

// buildTelemetry builds the tracing pipeline. A nil telemetry config leaves
// tracing off and returns a nil provider.
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

// instrumentDB adds GORM spans to the pool the App owns. A connection injected
// with WithDB is left untouched, so sharing it does not silently add callbacks.
func (a *App) instrumentDB(db *gorm.DB, own bool) error {
	if !own || db == nil {
		return nil
	}
	if err := a.telemetry.InstrumentGORM(db); err != nil {
		return fmt.Errorf("gofy: instrument database: %w", err)
	}
	return nil
}
