package middleware

import (
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
)

// NameHeaderPropagation 是 HeaderPropagation 在契约路由里使用的中间件别名。
const NameHeaderPropagation = "header-propagation"

// HeaderPropagation 把白名单请求头写入 request context，使 handler 与下游
// interface-service 客户端可以继续转发调用方身份和 trace id。
//
// 身份头仅在直连对端命中 trustedProxies（IP 或 CIDR）时采信；其他对端的身份
// 头会被丢弃，X-Client-Ip 改写为直连地址，防止外部请求伪造身份。因此本中间件
// 应尽量装在最前面，让后续中间件和 handler 读到的就是最终身份。
func HeaderPropagation(trustedProxies []string) gin.HandlerFunc {
	// 复制一份受信代理列表：调用方后续修改自己的切片不会影响已构建的中间件。
	proxies := append([]string(nil), trustedProxies...)
	return func(c *gin.Context) {
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
