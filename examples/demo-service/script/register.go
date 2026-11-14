package script

import (
	"context"

	"github.com/bingcool/gofy/apperr"
	gofyscript "github.com/bingcool/gofy/script"
	"github.com/spf13/pflag"
	"github.com/yourorg/demo-service/app/appio"
	"github.com/yourorg/demo-service/app/config"
)

// Register returns the demo scripts with the app/appio generation wrapped around
// the ones that share the service layer.
// Register 返回演示脚本；共用 service 层的那部分会套上 app/appio 装配代。
func Register(cfg *config.Config) []gofyscript.Script {
	return []gofyscript.Script{
		withIO(cfg, FixOrder()),
		GenUUID(cfg),
	}
}

// withIO starts the io module before the script and stops it before returning,
// so a script borrows one complete generation (db, redis, amqp, kafka,
// rocketmq, uuidint) exactly like a request does. Scripts declaring
// WithoutDatabase open only what they need themselves.
// withIO 在脚本前后启动、关闭 io 模块，脚本因此和请求一样借用完整的一代实例
// （db、redis、amqp、kafka、rocketmq、uuidint）；声明 WithoutDatabase 的脚本
// 不建数据库连接，由脚本自己打开所需实例。
func withIO(cfg *config.Config, s gofyscript.Script) gofyscript.Script {
	if s.WithoutDatabase {
		return s
	}
	run := s.Run
	s.Run = func(ctx context.Context, rt *gofyscript.Runtime, fs *pflag.FlagSet) error {
		if rt.DB == nil {
			return apperr.Internal("database is not configured")
		}
		module := appio.NewModule(rt, cfg.DB, cfg.Redis, cfg.AMQP, cfg.Kafka, cfg.RocketMQ, cfg.UUIDInt)
		if err := module.Start(ctx); err != nil {
			return apperr.Internal("start io module").WithCause(err)
		}
		runErr := run(ctx, rt, fs)
		if stopErr := module.Stop(ctx); stopErr != nil && runErr == nil {
			return apperr.Internal("stop io module").WithCause(stopErr)
		}
		return runErr
	}
	return s
}
