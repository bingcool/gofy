package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/bingcool/gofy/transport"
	"github.com/yourorg/demo-service/app/config"
	"github.com/yourorg/demo-service/app/module/order"
	appscript "github.com/yourorg/demo-service/script"
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

// enableNacosSyncConfig 控制启动时是否从 Nacos 同步配置并监听变更。
// 简单项目只维护本地 config.yaml 时设为 false；不创建 nacos.yaml 且不设置
// NACOS_* 环境变量时，配置本身也是禁用的，效果相同。
// 该开关只影响配置同步，不影响 nacos.yaml 中显式开启的服务注册。
const enableNacosSyncConfig = true

func main() {
	ctx, stop := gofy.SignalContext(context.Background())
	defer stop()

	nacosCfg, err := loadNacosConfig()
	if err != nil {
		log.Fatalf("load nacos config: %v", err)
	}
	if strings.TrimSpace(os.Getenv("NACOS_FILE")) == "" {
		nacosCfg.File = configPath()
	}
	// stop/version/script must work without reaching Nacos.
	pullConfig := enableNacosSyncConfig && nacosCfg.Enabled && isStartInvocation(os.Args[1:])
	if pullConfig {
		if _, err := nacos.Sync(ctx, nacosCfg); err != nil {
			log.Fatalf("sync nacos config: %v", err)
		}
	}

	cfg, err := config.Load(ctx, configPath())
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	reload := &reloader{
		nacosCfg: nacosCfg,
		watch:    pullConfig && nacosCfg.Watch,
		httpCfg:  cfg.HTTP,
	}

	opts := []gofy.Option{
		gofy.WithName("demo-service"),
		gofy.WithEnv(cfg.Env),
		gofy.WithLogConfig(cfg.Log),
		gofy.WithHTTP(cfg.HTTP),
		gofy.WithAPIPrefix("/api/v1"),
		gofy.WithPIDFile(cfg.PIDFile),
		gofy.WithDatabase(cfg.DB),
		gofy.WithNacosRegistration(nacosCfg),
		gofy.WithHealthEndpoints(true),
		gofy.WithModuleFactories(reload.factories(cfg)...),
		gofy.WithScripts(appscript.Register(cfg)...),
		gofy.WithVersionInfo(gofy.VersionInfo{
			Version:   version,
			Commit:    commit,
			BuildTime: buildTime,
		}),
		gofy.WithShutdownTimeout(10 * time.Second),
	}

	app, err := gofy.New(opts...)
	if err != nil {
		log.Fatalf("init application: %v", err)
	}
	// Publish the app before Execute starts the watcher: a config change may
	// arrive as soon as the watcher polls, and the callback needs to find it.
	reload.app.Store(app)

	if err := app.Execute(ctx); err != nil {
		app.Logger().Error("demo-service exit", zap.Error(err))
		os.Exit(gofy.ExitCode(err))
	}
}

func configPath() string {
	if p := os.Getenv("ORDER_CONFIG"); p != "" {
		return p
	}
	return "config.yaml"
}

// loadNacosConfig prefers the swoolefy-style nacos.yaml + application.yaml
// pair and falls back to NACOS_* environment variables when nacos.yaml is
// absent.
func loadNacosConfig() (nacos.Config, error) {
	cfg, err := nacos.Load("nacos.yaml", "application.yaml")
	if errors.Is(err, fs.ErrNotExist) {
		return nacos.FromEnv(), nil
	}
	return cfg, err
}

// isStartInvocation reports whether args select the foreground start (or no
// subcommand at all).
func isStartInvocation(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			if arg == "-h" || arg == "--help" {
				return false
			}
			continue
		}
		return arg == "start"
	}
	return true
}

// reloader rebuilds the modules from a freshly loaded config and applies them
// to the running application without restarting the process.
type reloader struct {
	// nacosCfg is fixed at startup: changing the Nacos connection itself still
	// requires a process restart.
	nacosCfg nacos.Config
	// watch reports whether every rebuild should include the watcher module.
	watch bool
	// httpCfg is the address the listener was bound to; Reload cannot move it.
	httpCfg gofy.HTTPConfig
	// app is stored after New and before Execute so the watcher callback can
	// reach the running application.
	app atomic.Pointer[gofy.App]
}

// factories returns the module list for cfg. It is used at startup and again
// on every Nacos change, so the rebuilt module set keeps the order routes and
// the config watcher.
//
// Reload starts this set before stopping the previous one, so a change briefly
// runs two watchers; the old watcher is cancelled once the swap completes.
func (r *reloader) factories(cfg *config.Config) []gofy.ModuleFactory {
	factories := []gofy.ModuleFactory{order.NewModuleFactory(cfg.Order)}
	if r.watch {
		factories = append(factories, func(rt *core.Runtime) (transport.Module, error) {
			return &nacosWatchModule{
				cfg:      r.nacosCfg,
				logger:   rt.Logger,
				onChange: r.reload,
			}, nil
		})
	}
	return factories
}

// reload re-reads the generated config and swaps the logger, database pool and
// modules in place. The HTTP listener keeps its startup address.
//
// It runs inside the watcher's change handler, after nacos.Watch has written
// the new content to disk, so config.Load sees the updated file.
func (r *reloader) reload(ctx context.Context) error {
	app := r.app.Load()
	if app == nil {
		return errors.New("gofy: application is not ready")
	}
	cfg, err := config.Load(ctx, configPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.HTTP.Host != r.httpCfg.Host || cfg.HTTP.Port != r.httpCfg.Port {
		app.Logger().Warn("http address changes require a restart; keeping the current listener",
			zap.String("current", net.JoinHostPort(r.httpCfg.Host, strconv.Itoa(r.httpCfg.Port))),
			zap.String("requested", net.JoinHostPort(cfg.HTTP.Host, strconv.Itoa(cfg.HTTP.Port))),
		)
	}
	return app.Reload(ctx, gofy.ReloadSpec{
		Env:             cfg.Env,
		Log:             &cfg.Log,
		Database:        &cfg.DB,
		ModuleFactories: r.factories(cfg),
	})
}

// nacosWatchModule keeps config.yaml in sync with Nacos and hands every change
// to the reloader instead of terminating the process.
type nacosWatchModule struct {
	cfg      nacos.Config
	logger   *zap.Logger
	onChange func(context.Context) error

	mu     sync.Mutex
	cancel context.CancelFunc
}

func (m *nacosWatchModule) Name() string { return "nacos-watch" }

func (m *nacosWatchModule) Register(*transport.Router) error { return nil }

func (m *nacosWatchModule) Start(ctx context.Context) error {
	// Tie the watcher to the application context: shutdown cancels it, and a
	// reload cancels it through Stop once the replacement is serving.
	watchCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	go func() {
		err := nacos.Watch(watchCtx, m.cfg,
			nacos.WithLogger(m.logger),
			nacos.WithOnChange(func(ctx context.Context, _ nacos.Result) error {
				if m.onChange == nil {
					return nil
				}
				return m.onChange(ctx)
			}),
		)
		if err != nil && !errors.Is(err, context.Canceled) {
			m.logger.Error("nacos watcher stopped", zap.Error(err))
		}
	}()
	return nil
}

// Stop only cancels the watcher. Reload runs inside its change handler, so
// waiting for the goroutine here would deadlock the reload.
func (m *nacosWatchModule) Stop(context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}
