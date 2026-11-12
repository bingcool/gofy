package middleware

import (
	"context"
	"time"

	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/frameworkcontext"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	appredis "github.com/yourorg/demo-service/app/io/redis"
	"gofy/gofy-interface-service/apicore/contracts/common/endpoint"
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
// 计数固定存放在 Redis：中间件在首次请求时用 redisstore 包住
// redis.NewIO(ctx, alias) 的客户端，见 redisLimiter。要换计数存储，只需替换
// redisLimiter 或注入不同的 RedisClient，契约与路由绑定都不用改。
type RateLimiter struct {
	// Responder 渲染 429 / 503；nil 时使用框架默认响应。App 装配根传入的
	// Responder 与全局错误信封一致，限流响应才能带同样的 code/msg/req_id。
	Responder support.Responder
	// RedisClient 按别名解析 Redis 客户端，默认 app/io/redis.NewIO：从
	// support/redis 注册表里取出该代安装的命名实例，实例未启用时返回错误。
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

// redisClient 返回客户端解析函数。默认走 app/io/redis.NewIO，它读取
// support/redis 注册表里该代安装的连接；装配根或测试可以通过字段覆盖。
func (r RateLimiter) redisClient() func(ctx context.Context, alias string) (*goredis.Client, error) {
	if r.RedisClient != nil {
		return r.RedisClient
	}
	return appredis.NewIO
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
