package middleware

import (
	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/bingcool/gofy/transport"
	"github.com/gin-gonic/gin"
)

// HeaderPropagation 把白名单请求头写入 request context，
// 使 handler 和下游 interface-service 客户端可以继续转发调用方身份和 trace id。
//
// 身份头仅在直连对端命中 trustedProxies（IP 或 CIDR）时采信；
// 其他对端的身份头会被丢弃，X-Client-Ip 改写为直连地址。
func HeaderPropagation(trustedProxies []string) gin.HandlerFunc {
	proxies := append([]string(nil), trustedProxies...)
	return func(c *gin.Context) {
		headers := headerpropagation.CaptureIncoming(
			c.Request.Header,
			c.GetString(transport.RequestIDKey),
			proxies,
			c.Request.RemoteAddr,
		)
		c.Request = c.Request.WithContext(
			headerpropagation.ContextWithHeaders(c.Request.Context(), headers),
		)
		c.Next()
	}
}
