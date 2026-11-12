package middleware

import (
	"context"
	"sync"

	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy/support"
	gofymiddleware "github.com/bingcool/gofy/support/middleware"
	"github.com/bingcool/gofy/support/ratelimit/redisstore"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
)

// redisLimiter 用 Redis 计数，替契约声明的这条路由维护滑窗。
//
// 为什么延迟构建：App 启动和 Reload 都先执行模块工厂并绑定路由，再执行各模块
// 的 Start；demo 的 Redis 连接由 app/io 模块在 Start 里打开并安装到
// support/redis 注册表。绑定阶段执行 NewIO 只会拿到“实例未配置”，所以客户端
// 与框架中间件必须在第一次请求时才构建。Reload 时新代中间件由新代容器创建，
// 首次请求解析到的自然是新代安装的客户端，旧连接随旧模块 Stop 退役。
//
// 并发与失败语义：
//   - 首次解析用互斥锁串行，成功后缓存，后续请求直接复用框架中间件；
//   - 解析失败不缓存，下一次请求重试，Redis 恢复后自动恢复限流；
//   - 解析失败、契约数值非法或 Redis 计数报错都返回 503，而不是放行本该
//     受限的请求（fail closed）。
type redisLimiter struct {
	// config 是契约翻译出的限流配置；Store 在首次解析时补齐。
	config support.RateLimitConfig
	// alias 是契约的 RateLimit.RedisAlias，缺省为 ratelimit。
	alias string
	// responder 渲染 429 / 503；nil 时使用框架默认响应。
	responder support.Responder
	// redis 按别名解析客户端，默认 app/io/redis.NewIO。
	redis func(ctx context.Context, alias string) (*goredis.Client, error)

	// mu 保护 handler 的首次构建；handler 非 nil 后不再重复构建。
	mu sync.Mutex
	// handler 是框架限流中间件，构建成功后由这条路由的所有请求共用。
	handler support.Middleware
}

// handle 是暴露给 gin 的入口：先确保框架中间件就绪，再交给它处理请求。
// 解析失败时直接返回 503 并终止链路，后续中间件和 handler 都不会执行。
func (l *redisLimiter) handle(c *gin.Context) {
	handler, err := l.resolve(c.Request.Context())
	if err != nil {
		rateLimiterUnavailable(c, l.responder)
		return
	}
	handler(c)
}

// resolve 返回这条路由的框架限流中间件，首次调用时构建。
//
// 构建分三步：① 按别名解析 Redis 客户端；② 用 redisstore 包成
// ratelimit.Store（空 prefix 使用 redisstore.DefaultPrefix，即
// gofy:ratelimit）；③ 交给 support/middleware.RateLimit 生成滑窗中间件，
// 由它负责计数、429 响应和 X-RateLimit-* / Retry-After 响应头。任一步失败
// 都原样返回，handle 会把当前请求按 503 处理；失败不写回 handler，下一次
// 请求会重新尝试。
func (l *redisLimiter) resolve(ctx context.Context) (support.Middleware, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.handler != nil {
		return l.handler, nil
	}
	client, err := l.redis(ctx, l.alias)
	if err != nil {
		return nil, err
	}
	// 空 prefix 使用 redisstore.DefaultPrefix。
	store, err := redisstore.New(client, "")
	if err != nil {
		return nil, err
	}
	config := l.config
	config.Store = store
	handler, err := gofymiddleware.RateLimit(config, l.responder)
	if err != nil {
		return nil, err
	}
	l.handler = handler
	return handler, nil
}

// rateLimiterUnavailable 渲染“限流不可用”的 503 响应。
//
// 与框架限流中间件在存储故障时的行为一致：不放行本该受限的请求。响应走 App
// 的 Responder，错误信封（code/msg/req_id）与其他错误保持一致。
func rateLimiterUnavailable(c *gin.Context, responder support.Responder) {
	if responder == nil {
		responder = support.NewResponder(support.ResponderConfig{})
	}
	responder.Error(c, apperr.Unavailable("rate limiter unavailable"))
	c.Abort()
}
