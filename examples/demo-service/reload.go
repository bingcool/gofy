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
	"github.com/yourorg/demo-service/app/appio"
	"github.com/yourorg/demo-service/app/config"
	"github.com/yourorg/demo-service/app/container"
	aimodule "github.com/yourorg/demo-service/app/module/ai"
)

// reloader rebuilds the modules from a freshly loaded config and applies them
// to the running application without restarting the process.
// reloader 用新加载的配置重建模块并应用到运行中的进程，无需重启；它只在
// Nacos 监听启用时存在。
type reloader struct {
	// nacosCfg is fixed at startup: changing the Nacos connection itself still
	// requires a process restart.
	// nacosCfg 在启动时固定：Reload 只换配置内容，改 Nacos 连接参数仍需重启进程。
	nacosCfg nacos.Config
	// watch reports whether every rebuild should include the watcher module.
	// watch 决定每次重建是否附带监听模块；关闭配置同步后新的一代不再监听变更。
	watch bool
	// httpCfg is the address the listener was bound to; Reload cannot move it.
	// httpCfg 是监听已绑定的地址；Reload 不重新绑定，只记下来给出告警。
	httpCfg gofy.HTTPConfig
	// envFromProcess 记录 GOFY_ENV 是否由进程显式设置。未显式设置时，
	// 热更新会让该变量跟随 config.yaml，保持 systemenv 与 Runtime.Env 一致。
	envFromProcess bool
	// app is stored after New and before Execute so the watcher callback can
	// reach the running application.
	// app 在 New 之后、Execute 之前存入，监听回调据此拿到运行中的应用句柄。
	app atomic.Pointer[gofy.App]
}

// factories returns the module list for cfg. It is used at startup and again
// on every Nacos change, so the rebuilt module set keeps the order routes and
// the config watcher.
//
// Reload starts this set before stopping the previous one, so a change briefly
// runs two watchers; the old watcher is cancelled once the swap completes.
// factories 按 cfg 返回本次的模块工厂列表，启动与每次 Nacos 变更都走这里，
// 重建后的集合必须仍包含业务路由和配置监听。
// Reload 先启动新集合再停旧集合，切换期间会短暂存在两个监听，旧监听在切换
// 完成后才取消。
func (r *reloader) factories(cfg *config.Config) []gofy.ModuleFactory {
	// 每个装配代新建一个容器：启动和每次 Reload 各得到一套仓库与 service，
	// 旧的模块实例继续服务在途请求。容器内部固定按 产品 → 订单 构建，模块
	// 工厂的排列顺序不再承载依赖顺序。
	ctr := container.NewContainer(cfg)
	factories := []gofy.ModuleFactory{
		appio.NewModuleFactory(cfg.DB, cfg.Redis, cfg.AMQP, cfg.Kafka, cfg.RocketMQ, cfg.UUIDInt, cfg.Lock),
	}
	factories = append(factories, ctr.ModuleFactories()...)
	// The AI module exists only when llm_ai.enabled is true. Registering it
	// unconditionally would turn "no LLM in this deployment" into a startup
	// error instead of a 404, and a missing section has to stay a non-event.
	// 只有 llm_ai.enabled 为 true 才注册 AI 模块。无条件注册会把"这个部署不接
	// 大模型"变成启动错误，而它本该只是 404；整段缺失更不该有任何副作用。
	if cfg.LLMAI.Enabled {
		factories = append(factories, aimodule.NewModuleFactory(cfg.LLMAI))
	}
	if r.watch {
		factories = append(factories, func(rt *core.Runtime) (support.Module, error) {
			return &nacosWatchModule{
				cfg:      r.nacosCfg,
				logger:   rt.Logger,
				onChange: r.reload,
				onDrain:  r.stopAccepting,
				onResume: r.resumeAccepting,
			}, nil
		})
	}
	return factories
}

// stopAccepting is the watcher's drain hook for the restart and exit
// strategies. It refuses new HTTP requests and deregisters from Nacos naming,
// so the watcher's grace period is spent on work already in flight. It only
// flips the App's gate: a full Shutdown here would stop the module that is
// calling this hook and deadlock waiting for it.
// stopAccepting 是 restart / exit 策略的摘流钩子：拒绝新的 HTTP 请求并从 Nacos
// 注销，让 watcher 的 grace period 只用于等在途任务收尾。它只翻 App 的闸门；
// 在这里执行完整 Shutdown 会停掉正在调用本钩子的模块，并等它退出而死锁。
func (r *reloader) stopAccepting(ctx context.Context) error {
	app := r.app.Load()
	if app == nil {
		return errors.New("gofy: application is not ready")
	}
	return app.StopAccepting(ctx)
}

// resumeAccepting is the counterpart of stopAccepting: when the process action
// failed, the app has to keep serving. Without it a failed exec would leave a
// live process deregistered and answering 503 until someone restarted it.
// resumeAccepting 是 stopAccepting 的逆操作：进程动作失败时应用必须继续服务。
// 没有它，失败的 exec 会留下一个活着、已注销、一直 503 的进程，直到有人重启它。
func (r *reloader) resumeAccepting(ctx context.Context) error {
	app := r.app.Load()
	if app == nil {
		return errors.New("gofy: application is not ready")
	}
	return app.ResumeAccepting(ctx)
}

// reload re-reads the generated config and swaps the logger, database pool and
// modules in place. The HTTP listener keeps its startup address.
//
// It runs inside the watcher's change handler, after nacos.Watch has written
// the new content to disk, so config.Load sees the updated file.
// reload 重新读取已落盘的配置，就地替换 logger、数据库连接池和模块；HTTP
// 监听沿用启动地址，改地址只告警不生效。
// 它运行在监听模块的变更回调里，此时 nacos.Watch 已把新内容写入磁盘，因此
// config.Load 读到的是更新后的文件。
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
