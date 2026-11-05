package main

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
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

func main() {
	ctx, stop := gofy.SignalContext(context.Background())
	defer stop()

	nacosCfg := nacos.FromEnv()
	if strings.TrimSpace(os.Getenv("NACOS_FILE")) == "" {
		nacosCfg.File = configPath()
	}
	// stop/version/script must work without reaching Nacos.
	pullConfig := nacosCfg.Enabled && isStartInvocation(os.Args[1:])
	if pullConfig {
		if _, err := nacos.Sync(ctx, nacosCfg); err != nil {
			log.Fatalf("sync nacos config: %v", err)
		}
	}

	cfg, err := config.Load(ctx, configPath())
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	opts := []gofy.Option{
		gofy.WithName("demo-service"),
		gofy.WithEnv(cfg.Env),
		gofy.WithLogConfig(cfg.Log),
		gofy.WithHTTP(cfg.HTTP),
		gofy.WithAPIPrefix("/api/v1"),
		gofy.WithPIDFile(cfg.PIDFile),
		gofy.WithDatabase(cfg.DB),
		gofy.WithHealthEndpoints(true),
		gofy.WithModuleFactories(
			order.NewModuleFactory(cfg.Order),
		),
		gofy.WithScripts(appscript.Register(cfg)...),
		gofy.WithVersionInfo(gofy.VersionInfo{
			Version:   version,
			Commit:    commit,
			BuildTime: buildTime,
		}),
		gofy.WithShutdownTimeout(10 * time.Second),
	}
	if nacosCfg.Enabled && pullConfig {
		opts = append(opts, gofy.WithModuleFactories(nacosWatchFactory(nacosCfg, cfg.PIDFile)))
	}

	app, err := gofy.New(opts...)
	if err != nil {
		log.Fatalf("init application: %v", err)
	}

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

// nacosWatchModule keeps config.yaml in sync with Nacos. Reporting the change
// and hanging up lets a supervisor restart the process with the new config.
type nacosWatchModule struct {
	cfg     nacos.Config
	pidFile string
	logger  *zap.Logger
}

func (m *nacosWatchModule) Name() string { return "nacos-watch" }

func (m *nacosWatchModule) Register(*transport.Router) error { return nil }

func (m *nacosWatchModule) Start(ctx context.Context) error {
	go func() {
		err := nacos.Watch(ctx, m.cfg,
			nacos.WithLogger(m.logger),
			nacos.WithRestartOnChange(m.pidFile),
		)
		if err != nil && !errors.Is(err, context.Canceled) {
			m.logger.Error("nacos watcher stopped", zap.Error(err))
		}
	}()
	return nil
}

func nacosWatchFactory(cfg nacos.Config, pidFile string) transport.ModuleFactory {
	return func(rt *core.Runtime) (transport.Module, error) {
		return &nacosWatchModule{cfg: cfg, pidFile: pidFile, logger: rt.Logger}, nil
	}
}
