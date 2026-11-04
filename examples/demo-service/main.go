package main

import (
	"context"
	"log"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/bingcool/gofy"
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

	cfg, err := config.Load(ctx, configPath())
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	app, err := gofy.New(
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
		gofy.WithShutdownTimeout(10*time.Second),
	)
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
