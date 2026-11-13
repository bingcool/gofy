package support

import (
	"errors"
	"time"

	"github.com/bingcool/gofy/support/ratelimit"
	"github.com/gin-gonic/gin"
)

// RateLimitKeyFunc derives the bucket key for a request.
// RateLimitKeyFunc 为请求推导限流桶的 key；返回空字符串时该请求不参与限流，
// 实现必须是纯函数，且不应读取响应体。
type RateLimitKeyFunc func(*gin.Context) string

// RateLimitConfig enables request limiting for module routes.
// RateLimitConfig 为模块路由启用请求限流；Store 的并发语义决定多实例部署下
// 限额是进程级还是全局级。
type RateLimitConfig struct {
	// Store counts requests. It is required; use ratelimit.NewMemoryStore for
	// a single process, or inject a shared implementation.
	// Store 负责计数，必填；单进程用 ratelimit.NewMemoryStore，多实例需注入共享实现，
	// 否则每个实例各算各的限额。
	Store ratelimit.Store
	// Limit is the number of requests allowed per Window per key. Required.
	// Limit 是每个 key 在每个 Window 内允许的请求数，必填且必须为正数。
	Limit int
	// Window is the sliding window length. Required.
	// Window 是滑动窗口长度，必填且必须为正数。
	Window time.Duration
	// KeyFunc groups requests into buckets. The default uses client IP, method
	// and route, so one hot endpoint cannot exhaust another.
	// KeyFunc 决定请求分桶方式；默认按客户端 IP、method 和路由组合，避免单个热点
	// 接口耗尽其他接口的配额。
	KeyFunc RateLimitKeyFunc
	// SkipPaths lists exact URL paths that bypass limiting.
	// SkipPaths 列出跳过限流的精确 URL 路径，不做前缀匹配。
	SkipPaths []string
	// FailOpen continues the request when Store returns an error. The default
	// fails closed with 503, which avoids removing protection during an outage.
	// FailOpen 为 true 时 Store 出错也放行请求；默认关闭并以 503 失败，避免存储
	// 故障期间失去保护。
	FailOpen bool
}

// Validate reports configurations the middleware cannot work with.
// Validate 报告中间件无法工作的配置；应在启动期调用，不要等首个请求才发现。
func (c RateLimitConfig) Validate() error {
	if c.Store == nil {
		return errors.New("support: rate limit store is required")
	}
	if c.Limit <= 0 {
		return errors.New("support: rate limit must be positive")
	}
	if c.Window <= 0 {
		return errors.New("support: rate limit window must be positive")
	}
	return nil
}
