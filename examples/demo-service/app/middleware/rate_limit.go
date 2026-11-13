package middleware

import (
	"context"
	"sync"
	"time"

	"github.com/bingcool/gofy-interface-service/apicore/contracts/common/endpoint"
	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/frameworkcontext"
	gofymiddleware "github.com/bingcool/gofy/support/middleware"
	"github.com/bingcool/gofy/support/ratelimit/redisstore"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	appio "github.com/yourorg/demo-service/app/io"
)

const (
	// apiKeyHeader 是 RateKeyAPIKey 读取的请求头，与多数网关的约定一致。
	apiKeyHeader = "X-API-Key"
	// defaultRedisAlias 是契约未声明 RateLimit.RedisAlias 时使用的实例别名。
	defaultRedisAlias = "ratelimit"
)

// RateLimiter 把契约声明的 Spec.RateLimit 翻译成 gofy 的限流中间件。
//
// 一条路由的完整链路：
//
//	adapter.BindGroup → BindOptions.RateLimit(spec) → RateLimiter.middleware
//	  → RateKey 选择计数主体 → redisLimiter（首次请求解析 Redis 连接）
//	  → support/middleware.RateLimit（滑窗统计、429 响应、限流响应头）
//
// 数值完全来自契约：Limit 是窗口内允许的请求数，WindowSeconds 是窗口长度，
// Key 决定计数主体（IP、用户、API Key 或整条路由），RedisAlias 决定计数
// 存放在哪个 Redis 命名实例。没有声明 Spec.RateLimit 的路由 adapter 不会
// 调用回调，也就完全不限流。
//
// 计数固定存放在 Redis：中间件在首次请求时用 redisstore 包住本代快照解析出的
// 客户端，见 redisLimiter。要换计数存储，只需替换 redisLimiter 或注入不同的
// RedisClient，契约与路由绑定都不用改。
type RateLimiter struct {
	// Responder 渲染 429 / 503；nil 时使用框架默认响应。App 装配根传入的
	// Responder 与全局错误信封一致，限流响应才能带同样的 code/msg/req_id。
	Responder support.Responder
	// RedisClient 按别名解析 Redis 客户端，默认取请求固定的 app/io 快照
	// （appio.FromContext(ctx).RedisNewIO）：解析不会跨 Reload 换到下一代。
	// 测试注入 miniredis 客户端，避免依赖真实 Redis。
	RedisClient func(ctx context.Context, alias string) (*goredis.Client, error)
}

// Callback 返回 gofyadapter.BindOptions.RateLimit 需要的回调。
//
// adapter 在绑定路由时只对声明了 Spec.RateLimit 的路由调用它，并把返回的
// 中间件装在路由中间件链最前面（分组中间件之后，BindOptions.Global、
// spec.Middleware 名字和 Routes[OperationID] 之前），超额请求在这些中间件和
// handler 执行前就被拒绝。回调本身不做 I/O：Redis 连接在首次请求时才解析，
// 因此模块工厂可以在 app/io 打开 Redis 之前完成路由绑定。
func (r RateLimiter) Callback() func(endpoint.Spec) support.Middleware {
	return r.middleware
}

// middleware 为一条声明了限流的路由构造中间件。
//
// 构造期只组装配置（数值、窗口、计数主体、别名、Redis 解析函数），不访问
// Redis；store 与框架限流中间件由 redisLimiter 在第一次请求时构建并缓存。
// 没有声明 RateLimit 时返回 nil；adapter 本不会调用到这里，这里判空是为了
// 防御直接调用回调的场景（例如测试）。
func (r RateLimiter) middleware(spec endpoint.Spec) support.Middleware {
	if spec.RateLimit == nil {
		return nil
	}
	limit := spec.RateLimit
	return (&redisLimiter{
		config: support.RateLimitConfig{
			Limit:   limit.Limit,
			Window:  time.Duration(limit.WindowSeconds) * time.Second,
			KeyFunc: rateLimitKeyFunc(limit.Key),
		},
		alias:     redisAlias(limit.RedisAlias),
		responder: r.Responder,
		redis:     r.redisClient(),
	}).handle
}

// redisAlias 应用契约默认值：RedisAlias 为空表示使用 ratelimit 实例，
// 与 demo config.yaml 里的 redis.ratelimit 一一对应。
func redisAlias(alias string) string {
	if alias == "" {
		return defaultRedisAlias
	}
	return alias
}

// redisClient 返回客户端解析函数。默认从 app/io 快照取本代 Redis 注册表，
// 装配根或测试可以通过字段覆盖。
func (r RateLimiter) redisClient() func(ctx context.Context, alias string) (*goredis.Client, error) {
	if r.RedisClient != nil {
		return r.RedisClient
	}
	return func(ctx context.Context, alias string) (*goredis.Client, error) {
		return appio.FromContext(ctx).RedisNewIO(ctx, alias)
	}
}

// rateLimitKeyFunc 按 RateKey 选择计数主体；未声明或未知的 Key 按客户端 IP
// 计数，与框架默认一致。所有桶键都带方法 + 路由，一条热点路由不会耗尽另一条
// 路由的额度。认证在分组层（group.Use("auth")）先于限流执行，所以 RateKeyUser
// 能读到已认证身份；取不到身份时退回客户端 IP，匿名请求不会挤进同一个桶。
func rateLimitKeyFunc(rateKey endpoint.RateKey) support.RateLimitKeyFunc {
	switch rateKey {
	case endpoint.RateKeyUser:
		return keyByUser
	case endpoint.RateKeyAPIKey:
		return keyByAPIKey
	case endpoint.RateKeyRoute:
		return keyByRoute
	default:
		return keyByIP
	}
}

func keyByIP(c *gin.Context) string {
	return bucketKey("ip", c.ClientIP(), c)
}

func keyByUser(c *gin.Context) string {
	return bucketKey("user", firstNonEmpty(frameworkcontext.UserID(c.Request.Context()), c.ClientIP()), c)
}

func keyByAPIKey(c *gin.Context) string {
	return bucketKey("api_key", firstNonEmpty(c.GetHeader(apiKeyHeader), c.ClientIP()), c)
}

// keyByRoute 不区分调用方，整条路由共用一个桶，适合保护单个下游资源。
func keyByRoute(c *gin.Context) string {
	return bucketKey("route", "", c)
}

// bucketKey 组装计数桶键：主体前缀 + 主体值 + 方法 + 路由。
//
// 带方法 + 路由是为了让每条路由拥有独立额度，避免一条热点路由耗尽同一主体
// 在另一条路由上的配额。路由取 gin 注册的模板路径（如 /orders/:id），同一
// 路由的不同 ID 共用一个桶；取不到模板时退回原始 URL 路径。
func bucketKey(rateKey, subject string, c *gin.Context) string {
	route := c.FullPath()
	if route == "" {
		route = c.Request.URL.Path
	}
	return rateKey + "|" + subject + "|" + c.Request.Method + "|" + route
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// redisLimiter 用 Redis 计数，替契约声明的这条路由维护滑窗。
//
// 为什么延迟构建：App 启动和 Reload 都先执行模块工厂并绑定路由，再执行各模块
// 的 Start；demo 的 Redis 连接由 app/io 模块在 Start 里打开并随快照一起发布。
// 绑定阶段解析只会拿到“快照/实例未配置”，所以客户端与框架中间件必须在第一次
// 请求时才构建。Reload 时新代中间件由新代容器创建，请求中间件固定的快照也是
// 新代，旧连接随旧模块 Stop 退役。
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
	// redis 按别名解析客户端，默认从请求固定的 app/io 快照解析。
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
