package script

import (
	"context"

	"github.com/bingcool/gofy/apperr"
	gofyscript "github.com/bingcool/gofy/script"
	"github.com/spf13/pflag"
	"github.com/yourorg/demo-service/app/appio"
	appdb "github.com/yourorg/demo-service/app/appio/db"
	"github.com/yourorg/demo-service/app/config"
)

// tenantOpenOptions mirrors the HTTP entry so scripts see the same isolation
// policy as requests. Both entries build the same generation; differing here
// would let a script read rows a request cannot.
// tenantOpenOptions 与 HTTP 入口一致，脚本因此和请求看到同样的隔离策略。两个入口
// 构建的是同一代，若这里不一致，脚本就能读到请求读不到的行。
func tenantOpenOptions(cfg *config.Config) []appdb.OpenOption {
	opts, enabled := cfg.Tenant.Options()
	if !enabled {
		return nil
	}
	return []appdb.OpenOption{appdb.WithTenant(opts)}
}

// Register returns the demo scripts with the app/appio generation wrapped around
// the ones that share the service layer.
// Register 返回演示脚本；共用 service 层的那部分会套上 app/appio 装配代。
func Register(cfg *config.Config) []gofyscript.Script {
	return []gofyscript.Script{
		withIO(cfg, FixOrder()),
		GenUUID(cfg),
		LockCheck(cfg),
		// No appio generation: the stream touches only the LLM endpoint.
		// 不需要 appio 装配代：这条流只访问 LLM 端点。
		AIStream(cfg),
	}
}

// withIO starts the io module before the script and stops it before returning,
// so a script borrows one complete generation (db, redis, amqp, kafka,
// rocketmq, uuidint) exactly like a request does. Scripts declaring
// WithoutDatabase open only what they need themselves.
//
// The generation is pinned explicitly instead of relying on the process-wide
// Current(): a reload in another entry can withdraw that global in the middle
// of a script, and a script must keep using the handles it started.
// withIO 在脚本前后启动、关闭 io 模块，脚本因此和请求一样借用完整的一代实例
// （db、redis、amqp、kafka、rocketmq、uuidint、lock）；声明 WithoutDatabase 的脚本
// 不建数据库连接，由脚本自己打开所需实例。
//
// 这里显式固定本代，而不是依赖进程级 Current()：别的入口触发的 Reload 可能在脚本
// 执行中途撤回全局值，而脚本必须继续用它自己启动的那一代句柄。
func withIO(cfg *config.Config, s gofyscript.Script) gofyscript.Script {
	if s.WithoutDatabase {
		return s
	}
	run := s.Run
	s.Run = func(ctx context.Context, rt *gofyscript.Runtime, fs *pflag.FlagSet) error {
		if rt.DB == nil {
			return apperr.Internal("database is not configured")
		}
		module := appio.NewModule(rt, cfg.DB, cfg.Redis, cfg.AMQP, cfg.Kafka, cfg.RocketMQ, cfg.UUIDInt, cfg.Lock, tenantOpenOptions(cfg)...)
		if err := module.Start(ctx); err != nil {
			return apperr.Internal("start io module").WithCause(err)
		}
		runErr := run(appio.WithSnapshot(ctx, module.Snapshot()), rt, fs)
		if stopErr := module.Stop(ctx); stopErr != nil && runErr == nil {
			return apperr.Internal("stop io module").WithCause(stopErr)
		}
		return runErr
	}
	return s
}
