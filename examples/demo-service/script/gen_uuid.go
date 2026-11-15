package script

import (
	"context"
	"fmt"
	"time"

	"github.com/bingcool/gofy/apperr"
	gofyscript "github.com/bingcool/gofy/script"
	gofyredis "github.com/bingcool/gofy/support/redis"
	goredis "github.com/redis/go-redis/v9"
	"github.com/spf13/pflag"
	appuuidint "github.com/yourorg/demo-service/app/appio/uuidint"
	"github.com/yourorg/demo-service/app/config"
	"go.uber.org/zap"
)

// GenUUID generates distributed IDs with support/uuidint.
//
// 脚本只校验 Redis 与生成器，不碰 service 层，因此声明 WithoutDatabase 跳过
// app/appio 装配代（否则会被迫连接 MySQL、Kafka 等无关实例），自己按配置打开选中
// 的 redis 实例，把客户端直接交给生成器（与 app/appio 相同的 WithClient 路径）。
func GenUUID(cfg *config.Config) gofyscript.Script {
	return gofyscript.Script{
		Name:            "gen-uuid",
		Short:           "generate distributed ids from Redis",
		WithoutDatabase: true,
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
			// The script skips the appio module, so there is no per-generation
			// Redis Set to pull clients from: it opens both instances itself.
			// 脚本跳过 appio 装配代，没有本代 Redis Set 可取客户端，因此主备连接都自己打开。
			instances := map[string]gofyredis.Config{alias: inst.Config}
			fallbackName := uidCfg.FallbackRedisInstanceName()
			if fallbackName != "" {
				if fallbackName == alias {
					return apperr.BadRequest("uuidint fallback_redis_instance_name must differ from --redis")
				}
				fb, ok := cfg.Redis[fallbackName]
				if !ok || !fb.Enabled {
					return apperr.BadRequest(fmt.Sprintf("fallback redis instance %q is not enabled in config", fallbackName))
				}
				instances[fallbackName] = fb.Config
			}
			reg, err := gofyredis.Configure(instances)
			if err != nil {
				return apperr.Internal("configure redis").WithCause(err)
			}
			defer func() { _ = reg.Close() }()
			client, err := reg.NewIO(ctx, alias)
			if err != nil {
				return apperr.Internal("connect redis").WithCause(err)
			}
			var fallbackClient *goredis.Client
			if fallbackName != "" {
				fallbackClient, err = reg.NewIO(ctx, fallbackName)
				if err != nil {
					return apperr.Internal("connect fallback redis").WithCause(err)
				}
			}

			usePool, _ := fs.GetBool("pool")
			if !usePool {
				uidCfg.PoolSize = 0
			}
			wait, _ := fs.GetDuration("wait")

			uidCfg.RedisInstance = alias
			uidCfg.Key = key
			set, err := appuuidint.Open(uidCfg, rt.Logger, client, fallbackClient)
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
