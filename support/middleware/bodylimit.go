package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// NameBodyLimit 是 BodyLimit 在契约路由里使用的中间件别名。
const NameBodyLimit = "body-limit"

// BodyLimit 限制请求体大小：把 Body 包成 http.MaxBytesReader，读取超过
// maxBytes 时返回 *http.MaxBytesError，由请求绑定层映射成 413。
//
// maxBytes <= 0 表示不限制；Body 为 nil（如 GET）时不做任何处理。必须装在
// 读取 body 的中间件和 handler 之前，否则限制不会生效。
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if maxBytes > 0 && c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}
