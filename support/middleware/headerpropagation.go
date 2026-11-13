package middleware

import (
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
)

// NameHeaderPropagation is the middleware alias HeaderPropagation uses on
// contract routes.
// NameHeaderPropagation 是 HeaderPropagation 在契约路由里使用的中间件别名。
const NameHeaderPropagation = "header-propagation"

// HeaderPropagation writes allowlisted request headers into the request context
// so handlers and downstream interface-service clients can keep forwarding the
// caller identity and trace id.
//
// Identity headers are trusted only when the direct peer matches
// trustedProxies (IP or CIDR); identity headers from any other peer are
// dropped and X-Client-Ip is rewritten to the direct address, which keeps
// external requests from forging an identity. Install this middleware as early
// as possible, so later middleware and handlers read the final identity.
// HeaderPropagation 把白名单请求头写入 request context，使 handler 与下游
// interface-service 客户端可以继续转发调用方身份和 trace id。
//
// 身份头仅在直连对端命中 trustedProxies（IP 或 CIDR）时采信；其他对端的身份
// 头会被丢弃，X-Client-Ip 改写为直连地址，防止外部请求伪造身份。因此本中间件
// 应尽量装在最前面，让后续中间件和 handler 读到的就是最终身份。
func HeaderPropagation(trustedProxies []string) gin.HandlerFunc {
	// Copy the trusted-proxy list: a caller mutating its own slice later must not
	// affect the middleware that was already built.
	// 复制一份受信代理列表：调用方后续修改自己的切片不会影响已构建的中间件。
	proxies := append([]string(nil), trustedProxies...)
	return func(c *gin.Context) {
		// With tracing enabled prefer the distributed trace id so downstream keeps
		// using the same trace; otherwise fall back to req_id and preserve the
		// existing X-Trace-Id behaviour.
		// 启用 tracing 时优先用分布式 trace id，下游继续用同一个 trace；
		// 未启用时退回 req_id，保持既有的 X-Trace-Id 行为。
		traceID := c.GetString(support.RequestIDKey)
		if sc := trace.SpanContextFromContext(c.Request.Context()); sc.IsValid() {
			traceID = sc.TraceID().String()
		}
		headers := headerpropagation.CaptureIncoming(
			c.Request.Header,
			traceID,
			proxies,
			c.Request.RemoteAddr,
		)
		c.Request = c.Request.WithContext(
			headerpropagation.ContextWithHeaders(c.Request.Context(), headers),
		)
		c.Next()
	}
}
