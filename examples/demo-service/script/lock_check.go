package script

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bingcool/gofy/apperr"
	gofyscript "github.com/bingcool/gofy/script"
	gofylock "github.com/bingcool/gofy/support/lock"
	gofyredis "github.com/bingcool/gofy/support/redis"
	"github.com/spf13/pflag"
	"github.com/yourorg/demo-service/app/config"
	"go.uber.org/zap"
)

// LockCheck acquires a distributed lock with support/lock and releases it.
//
// 脚本只碰 Redis，不碰 service 层，因此声明 WithoutDatabase 跳过 app/appio 装配代，
// 自己按配置打开选中的 redis 实例，把客户端直接交给 Locker（与请求路径相同的
// WithClient 路径）。两个终端分别跑 `--hold 30s` 和 `--try` 就能看到互斥效果。
func LockCheck(cfg *config.Config) gofyscript.Script {
	return gofyscript.Script{
		Name:            "lock-check",
		Short:           "acquire and release a distributed lock stored in Redis",
		WithoutDatabase: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("key", "check", "lock key, without the configured prefix")
			fs.Duration("ttl", 0, "lock TTL (default: lock.expiry)")
			fs.Duration("hold", 0, "keep the lock this long before releasing it")
			fs.String("token", "", "caller-supplied token (default: random)")
			fs.String("redis", "", "redis instance alias (default: lock.redis_instance_name)")
			fs.Bool("try", false, "use TryLock (one attempt) instead of Lock")
		},
		Run: func(ctx context.Context, rt *gofyscript.Runtime, fs *pflag.FlagSet) error {
			lockCfg := cfg.Lock
			if !lockCfg.Enabled {
				return apperr.BadRequest("lock is disabled in config")
			}
			alias, _ := fs.GetString("redis")
			if alias == "" {
				alias = lockCfg.RedisInstanceName()
			}
			inst, ok := cfg.Redis[alias]
			if !ok || !inst.Enabled {
				return apperr.BadRequest(fmt.Sprintf("redis instance %q is not enabled in config", alias))
			}
			// The script skips the appio module, so there is no per-generation
			// Redis Set to pull a client from: it opens the instance itself.
			// 脚本跳过 appio 装配代，没有本代 Redis Set 可取客户端，因此自己打开实例。
			reg, err := gofyredis.Configure(map[string]gofyredis.Config{alias: inst.Config})
			if err != nil {
				return apperr.Internal("configure redis").WithCause(err)
			}
			defer func() { _ = reg.Close() }()
			client, err := reg.NewIO(ctx, alias)
			if err != nil {
				return apperr.Internal("connect redis").WithCause(err)
			}

			if ttl, _ := fs.GetDuration("ttl"); ttl > 0 {
				lockCfg.Expiry = ttl
			}
			locker, err := gofylock.Open(lockCfg, client, gofylock.WithErrorHandler(func(err error) {
				rt.Logger.Warn("redis lock failed", zap.String("redis", alias), zap.Error(err))
			}))
			if err != nil {
				return apperr.Internal("build locker").WithCause(err)
			}
			if locker == nil {
				return apperr.BadRequest("lock is disabled in config")
			}

			key, _ := fs.GetString("key")
			var opts []gofylock.LockOption
			if token, _ := fs.GetString("token"); token != "" {
				opts = append(opts, gofylock.WithLockToken(token))
			}
			try, _ := fs.GetBool("try")

			var held *gofylock.Mutex
			if try {
				held, err = locker.TryLock(ctx, key, opts...)
			} else {
				held, err = locker.Lock(ctx, key, opts...)
			}
			if errors.Is(err, gofylock.ErrAlreadyHeld) {
				return apperr.Conflict(fmt.Sprintf("lock %q is held by another process", key))
			}
			if err != nil {
				return apperr.Internal("acquire lock").WithCause(err)
			}

			rt.Logger.Info("lock acquired",
				zap.String("redis", alias),
				zap.String("key", held.Key()),
				zap.String("redis_key", held.RedisKey()),
				zap.String("token", held.Token()),
				zap.Duration("ttl", held.Expiry()),
				zap.Time("until", held.Until()),
			)
			fmt.Printf("acquired %s (redis key %s, token %s, until %s)\n",
				held.Key(), held.RedisKey(), held.Token(), held.Until().Format(time.RFC3339Nano))

			if hold, _ := fs.GetDuration("hold"); hold > 0 {
				timer := time.NewTimer(hold)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
			if err := held.Unlock(ctx); err != nil {
				return apperr.Internal("release lock").WithCause(err)
			}
			fmt.Println("released", held.Key())
			return nil
		},
	}
}
