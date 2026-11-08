// Package middleware contains business middleware for demo-service.
package middleware

import (
	"net/http"
	"strings"

	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
)

// NameAuth 是 Auth 在契约路由里使用的中间件别名。
const NameAuth = "auth"

// Auth verifies a bearer token. Failures use the same envelope shape as the
// framework responder so clients see a consistent payload.
func Auth(verify func(token string) error) support.Middleware {
	return func(c *gin.Context) {
		token := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if err := verify(token); err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code":   40100,
				"msg":    "unauthorized",
				"data":   nil,
				"req_id": c.GetString(support.RequestIDKey),
			})
			return
		}
		c.Next()
	}
}
