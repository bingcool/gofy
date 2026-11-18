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
			// The budget covers both the primary attempt and the fallback: a
			// one-attempt deadline would be consumed by the primary timeout,
			// leaving shouldDegrade a cancelled context and no second try.
			// DrainContext 让在途调用跨过停机，日志里记录的是真实结果而不是被取消。
			// 预算覆盖主调用与兜底两次尝试：只给一次尝试的 deadline 会被主调用超时
			// 耗尽，shouldDegrade 只会看到已取消的 context，兜底不会发生。
			callCtx, cancel := context.WithTimeout(gofyworker.DrainContext(ctx), clients.CallBudget())
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
			// A 2xx with no data payload decodes to a nil result and no error:
			// the client treats "empty data" as a valid empty answer (that is
			// how a 204 or data:null route comes back). A handler that reads
			// res.AuthRequired without this check panics, and the ticker's
			// recover would report it as a worker panic every interval.
			// 2xx 但没有 data 载荷时解码结果是 nil 且没有错误：客户端把"空 data"当作
			// 合法的空答复（204 或 data:null 的路由就是这样返回的）。handler 不判空就
			// 读 res.AuthRequired 会 panic，而 ticker 的 recover 会把它当成 worker
			// panic 每个周期报一次。
			if res == nil {
				lg.Warn("discovery check got an empty payload",
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
