package route

import (
	"github.com/gin-contrib/requestid"
	"github.com/gin-gonic/gin"
)

// SetupProductRouter 设置产品路由
func SetupProductRouter(router *gin.Engine) {
	// 添加中间件
	// 启用 RequestID 中间件（默认生成 UUID）
	router.Use(requestid.New())

	router.GET("/ping", func(ctx *gin.Context) {
		ctx.JSON(200, gin.H{
			"message": "pong",
		})
	})
}
