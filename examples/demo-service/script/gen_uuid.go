package script

import (
	"context"
	"fmt"
	"time"

	"github.com/bingcool/gofy/apperr"
	gofyscript "github.com/bingcool/gofy/script"
	gofyredis "github.com/bingcool/gofy/support/redis"
	"github.com/spf13/pflag"
	"github.com/yourorg/demo-service/app/config"
	appuuidint "github.com/yourorg/demo-service/app/io/uuidint"
	"go.uber.org/zap"
)

// GenUUID generates distributed IDs with support/uuidint.
//
// 脚本模式不经过模块生命周期，app/io 的 redis 注册表不会启动；这里按配置把
// 选中的 redis 实例注册进 support/redis，再走与 app/io 相同的 WithInstance
// 别名路径，因此脚本能验证真实配置、而不依赖 HTTP 进程。
func GenUUID(cfg *config.Config) gofyscript.Script {
	return gofyscript.Script{
		Name:  "gen-uuid",
		Short: "generate distributed ids from Redis",
		Flags: func(fs *pflag.FlagSet) {
			fs.Int("count", 5, "how many ids to generate")
			fs.String("redis", "", "redis instance alias (default: uuidint.redis_instance_name)")
			fs.String("key", "", "counter key (default: uuidint.key)")
			fs.Bool("pool", false, "use the configured local prefetch pool")
			fs.Duration("wait", 0, "wait before allocating (lets the pool refill on its ticker)")
		},
		Run: func(ctx context.Context, rt *gofyscript.Runtime, fs *pflag.FlagSet) error {
			uidCfg := cfg.UUIDInt
			if !uidCfg.Enabled {
				return apperr.BadRequest("uuidint is disabled in config")
			}
			alias, _ := fs.GetString("redis")
			if alias == "" {
				alias = uidCfg.RedisInstance
			}
			key, _ := fs.GetString("key")
			if key == "" {
				key = uidCfg.Key
			}
			count, _ := fs.GetInt("count")

			inst, ok := cfg.Redis[alias]
			if !ok || !inst.Enabled {
				return apperr.BadRequest(fmt.Sprintf("redis instance %q is not enabled in config", alias))
			}
			reg, err := gofyredis.Configure(map[string]gofyredis.Config{alias: inst.Config})
			if err != nil {
				return apperr.Internal("configure redis").WithCause(err)
			}
			defer func() { _ = reg.Close() }()

			usePool, _ := fs.GetBool("pool")
			if !usePool {
				uidCfg.PoolSize = 0
			}
			wait, _ := fs.GetDuration("wait")

			uidCfg.RedisInstance = alias
			uidCfg.Key = key
			set, err := appuuidint.Open(uidCfg, rt.Logger)
			if err != nil {
				return apperr.Internal("build uuidint generator").WithCause(err)
			}
			defer func() { _ = set.Close() }()
			if err := set.Start(ctx); err != nil {
				return apperr.Internal("start uuidint pool").WithCause(err)
			}
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}

			gen := set.Generator()
			ids, err := gen.GetBatch(ctx, count)
			if err != nil {
				return apperr.Internal("generate ids").WithCause(err)
			}
			rt.Logger.Info("uuid ids generated",
				zap.String("redis", alias),
				zap.String("key", key),
				zap.Bool("pool", usePool),
				zap.Int("count", len(ids)),
			)
			for _, id := range ids {
				fmt.Println(id)
			}
			return nil
		},
	}
}
