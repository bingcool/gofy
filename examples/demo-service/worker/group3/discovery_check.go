package group3

import (
	"context"
	"time"

	"go.uber.org/zap"

	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/yourorg/demo-service/serviceclients"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
)

// DiscoveryCheckConfig paces the check. No yaml tags: the interval is a property
// of the loop and lives next to it, like group2's summary defaults.
// DiscoveryCheckConfig 控制巡检节奏。没有 yaml 标签：间隔是循环自己的属性，就和
// group2 的汇总参数一样写在旁边。
type DiscoveryCheckConfig struct {
	// IntervalMS is the pause between two calls, in milliseconds.
	// IntervalMS 是两次调用之间的间隔，单位毫秒。
	IntervalMS uint32
}

// discoveryCheckDefaults calls every five seconds: fast enough to watch a
// rolling restart, slow enough not to fill the log.
// discoveryCheckDefaults 每五秒调用一次：足以观察滚动重启，又不会刷满日志。
var discoveryCheckDefaults = DiscoveryCheckConfig{IntervalMS: 5_000}

// newDiscoveryCheckWorker calls GET /api/v1/order/config through the injected
// client and logs the outcome. A failure is logged, not returned: a registry
// outage must not kill the worker that is supposed to report it. The next tick
// retries, and the resolver's own throttle keeps the outage from turning into
// one Nacos timeout per tick.
// newDiscoveryCheckWorker 通过注入的客户端调用 GET /api/v1/order/config 并记录结果。
// 失败只记日志、不返回错误：注册中心故障不该杀掉负责报告它的 worker。下一个 tick
// 会重试，而 resolver 自己的节流让这次故障不会变成每个 tick 一次超时。
func newDiscoveryCheckWorker(clients *serviceclients.Clients, cfg DiscoveryCheckConfig) gofyworker.Worker {
	demo, source := clients.DemoService(), clients.Source()
	interval := cfg.IntervalMS
	if interval == 0 {
		interval = 5_000
	}
	return gofyworker.Func("discovery-check", 1, func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		lg := zap.NewNop()
		if ok && rt.Logger != nil {
			lg = rt.Logger
		}
		lg.Info("discovery check started",
			zap.Uint32("interval_ms", interval),
			zap.String("client", source),
		)
		defer lg.Info("discovery check stopped")

		return gofyworker.Ticker(ctx, interval, func(ctx context.Context) error {
			// DrainContext keeps an in-flight call alive through a shutdown so
			// the log line shows the real outcome instead of a cancellation.
			// DrainContext 让在途调用跨过停机，日志里记录的是真实结果而不是被取消。
			callCtx, cancel := context.WithTimeout(gofyworker.DrainContext(ctx), 5*time.Second)
			defer cancel()

			started := time.Now()
			res, err := demo.GetConfig(callCtx, &orderrequest.GetConfigRequest{})
			if err != nil {
				lg.Warn("discovery check failed",
					zap.Error(err),
					zap.Duration("elapsed", time.Since(started)),
				)
				return nil
			}
			lg.Info("discovery check ok",
				zap.Bool("auth_required", res.AuthRequired),
				zap.Duration("elapsed", time.Since(started)),
			)
			return nil
		})
	})
}
