package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// NameBodyLimit 是 BodyLimit 在契约路由里使用的中间件别名。
const NameBodyLimit = "body-limit"

// BodyLimit caps the request body size. Reads beyond the limit fail with
// *http.MaxBytesError, which the binder maps to 413.
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if maxBytes > 0 && c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}
