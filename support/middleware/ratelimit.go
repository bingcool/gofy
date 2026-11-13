package middleware

import (
	"math"
	"strconv"

	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
)

// NameRateLimit is the middleware alias RateLimit uses on contract routes.
// NameRateLimit 是 RateLimit 在契约路由里使用的中间件别名。
const NameRateLimit = "rate-limit"

// RateLimit allows at most cfg.Limit requests per cfg.Window, with counting
// delegated to cfg.Store (in-memory or Redis).
//
// Every response carries X-RateLimit-Limit / Remaining / Reset so clients can
// back off early; a rejected request additionally carries Retry-After and gets
// the standard 429 envelope, and the rejected attempt is counted and reported in
// the headers as well.
//
// A store failure fails closed by default: it returns 503 instead of letting
// traffic through, so an outage of the counter cannot hammer downstream. Set
// FailOpen to true when availability matters more than exact counting.
//
// The bucket identity comes from cfg.KeyFunc; without one, requests are bucketed
// by "client IP + method + route template". Paths matching SkipPaths are not
// counted at all and get no rate limit headers.
// RateLimit 在 cfg.Window 窗口内限制请求数不超过 cfg.Limit，计数委托给
// cfg.Store（内存或 Redis 实现）。
//
// 每个响应都带 X-RateLimit-Limit / Remaining / Reset，客户端可以据此提前
// 退避；被拒绝的请求额外带 Retry-After 并返回 429 标准错误包络，被拒绝的
// 那次请求同样写入计数头。
//
// 存储故障默认 fail closed：返回 503 而不是放行，避免计数失效时流量打穿
// 下游；如果确实更看重接口可用性，把 FailOpen 设为 true 即可改为放行。
//
// 计数主体由 cfg.KeyFunc 决定；未配置时按「客户端 IP + 方法 + 路由模板」
// 分桶。SkipPaths 命中的路径完全不计流，也不会写计数头。
func RateLimit(cfg support.RateLimitConfig, responder support.Responder) (gin.HandlerFunc, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if responder == nil {
		responder = support.NewResponder(support.ResponderConfig{})
	}
	keyFunc := cfg.KeyFunc
	if keyFunc == nil {
		keyFunc = defaultRateLimitKey
	}
	skip := make(map[string]struct{}, len(cfg.SkipPaths))
	for _, p := range cfg.SkipPaths {
		skip[p] = struct{}{}
	}

	return func(c *gin.Context) {
		if _, ok := skip[c.Request.URL.Path]; ok {
			c.Next()
			return
		}
		res, err := cfg.Store.Allow(c.Request.Context(), keyFunc(c), cfg.Limit, cfg.Window)
		if err != nil {
			if cfg.FailOpen {
				c.Next()
				return
			}
			responder.Error(c, apperr.Unavailable("rate limiter unavailable"))
			c.Abort()
			return
		}

		c.Header("X-RateLimit-Limit", strconv.Itoa(res.Limit))
		c.Header("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
		if !res.ResetAt.IsZero() {
			c.Header("X-RateLimit-Reset", strconv.FormatInt(res.ResetAt.Unix(), 10))
		}
		if res.Allowed {
			c.Next()
			return
		}
		retry := int(math.Ceil(res.RetryAfter.Seconds()))
		if retry < 1 {
			retry = 1
		}
		c.Header("Retry-After", strconv.Itoa(retry))
		responder.Error(c, apperr.TooManyRequests("too many requests"))
		c.Abort()
	}, nil
}

// defaultRateLimitKey is the default bucket key: client IP + method + route
// template. Method and route are included so every route has its own quota and
// one hot route cannot drain the same client's quota on other routes; the route
// is the template registered with gin (such as /orders/:id), so different IDs of
// one route share a bucket, falling back to the raw URL path when no template is
// available.
// defaultRateLimitKey 是默认分桶键：客户端 IP + 方法 + 路由模板。带方法和
// 路由是为了让每条路由拥有独立额度，一条热点路由不会耗尽同一客户端在其它
// 路由上的配额；路由取 gin 注册的模板路径（如 /orders/:id），同一路由的不同
// ID 共用一个桶，取不到模板时退回原始 URL 路径。
func defaultRateLimitKey(c *gin.Context) string {
	route := c.FullPath()
	if route == "" {
		route = c.Request.URL.Path
	}
	return c.ClientIP() + "|" + c.Request.Method + "|" + route
}
