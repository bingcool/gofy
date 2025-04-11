package middleware

import "github.com/gin-gonic/gin"
import "github.com/gin-contrib/requestid"

// SetGlobalRequestId 设置全局RequestId
func SetGlobalRequestId(router *gin.Engine) {
	router.Use(customerRequestId())
}

// customerRequestId 自定义RequestId
func customerRequestId() gin.HandlerFunc {
	return requestid.New()
}
