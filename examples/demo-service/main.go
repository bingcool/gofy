package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/core/systemenv"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/bingcool/gofy/transport"
	"github.com/yourorg/demo-service/app/config"
	appio "github.com/yourorg/demo-service/app/io"
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
const enableNacosSyncConfig = false

func main() {
	ctx, stop := gofy.SignalContext(context.Background())
	defer stop()

	nacosCfg, err := loadNacosConfig()
	if err != nil {
		fatalf("load nacos config: %v", err)
	}
	if strings.TrimSpace(os.Getenv("NACOS_FILE")) == "" {
		nacosCfg.File = configPath()
	}
	// stop/version/script must work without reaching Nacos.
	pullConfig := enableNacosSyncConfig && nacosCfg.Enabled && isStartInvocation(os.Args[1:])
	if pullConfig {
		if _, err := nacos.Sync(ctx, nacosCfg); err != nil {
			fatalf("sync nacos config: %v", err)
		}
	}

	cfg, err := config.Load(ctx, configPath())
	if err != nil {
		fatalf("load config: %v", err)
	}
	// 进程显式设置了 GOFY_ENV 时以它为准；否则回落到 config.yaml 的 env，
	// 让 systemenv 与 Runtime.Env 读到的环境保持一致。
	envFromProcess := strings.TrimSpace(os.Getenv("GOFY_ENV")) != ""
	if !envFromProcess {
		if err := os.Setenv("GOFY_ENV", cfg.Env); err != nil {
			fatalf("set GOFY_ENV: %v", err)
		}
	}

	reload := &reloader{
		nacosCfg:       nacosCfg,
		watch:          pullConfig && nacosCfg.Watch,
		httpCfg:        cfg.HTTP,
		envFromProcess: envFromProcess,
	}

	opts := []gofy.Option{
		gofy.WithName("demo-service"),
		gofy.WithEnv(systemenv.GetEnv()),
		gofy.WithLogConfig(cfg.Log),
		gofy.WithHTTP(cfg.HTTP),
		gofy.WithAPIPrefix("/api/v1"),
		gofy.WithPIDFile(cfg.PIDFile),
		gofy.WithDatabase(cfg.Database(config.DefaultDatabase)),
		gofy.WithNacosRegistration(nacosCfg),
		gofy.WithHealthEndpoints(true),
		// 把入口请求的白名单 Header 注入上下文，供 demoserviceclient 转发到下游；
		// 身份头只有在 WithTrustedProxies 命中的代理转发时才采信。
		gofy.WithHeaderPropagation(true),
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
		fatalf("init application: %v", err)
	}
	// Publish the app before Execute starts the watcher: a config change may
	// arrive as soon as the watcher polls, and the callback needs to find it.
	reload.app.Store(app)

	if err := app.Execute(ctx); err != nil {
		app.Logger().Error("demo-service exit", zap.Error(err))
		os.Exit(gofy.ExitCode(err))
	}
}

// fatalf 保留 log 的时间戳，把启动阶段的致命错误以红色写到 stderr 后退出。
// 输出不是终端、设置了 NO_COLOR 或在 Windows 上时保持无色。
func fatalf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if stderrColor() {
		msg = "\x1b[31m" + msg + "\x1b[0m"
	}
	log.Print(msg)
	os.Exit(1)
}

func stderrColor() bool {
	if os.Getenv("NO_COLOR") != "" || runtime.GOOS == "windows" {
		return false
	}
	info, err := os.Stderr.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
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
	// envFromProcess 记录 GOFY_ENV 是否由进程显式设置。未显式设置时，
	// 热更新会让该变量跟随 config.yaml，保持 systemenv 与 Runtime.Env 一致。
	envFromProcess bool
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
	factories := []gofy.ModuleFactory{
		appio.NewModuleFactory(cfg.DB, cfg.Redis),
		order.NewModuleFactory(cfg.Order),
	}
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
	env := cfg.Env
	if !r.envFromProcess {
		if err := os.Setenv("GOFY_ENV", cfg.Env); err != nil {
			return fmt.Errorf("set GOFY_ENV: %w", err)
		}
		env = systemenv.GetEnv()
	}
	dbCfg := cfg.Database(config.DefaultDatabase)
	return app.Reload(ctx, gofy.ReloadSpec{
		Env:             env,
		Log:             &cfg.Log,
		Database:        &dbCfg,
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
