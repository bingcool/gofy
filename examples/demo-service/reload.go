package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/core/systemenv"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/yourorg/demo-service/app/config"
	"github.com/yourorg/demo-service/app/container"
	appio "github.com/yourorg/demo-service/app/io"
)

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
	// 每个装配代新建一个容器：启动和每次 Reload 各得到一套仓库与 service，
	// 旧的模块实例继续服务在途请求。容器内部固定按 产品 → 订单 构建，模块
	// 工厂的排列顺序不再承载依赖顺序。
	ctr := container.NewContainer()
	factories := []gofy.ModuleFactory{
		appio.NewModuleFactory(cfg.DB, cfg.Redis, cfg.AMQP, cfg.Kafka, cfg.RocketMQ, cfg.UUIDInt),
	}
	factories = append(factories, ctr.ModuleFactories()...)
	if r.watch {
		factories = append(factories, func(rt *core.Runtime) (support.Module, error) {
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
		Env:                  env,
		Log:                  &cfg.Log,
		Database:             &dbCfg,
		ModuleFactories:      r.factories(cfg),
		MiddlewareRegistrars: []gofy.MiddlewareRegistrar{middlewareRegistrar(cfg)},
	})
}
