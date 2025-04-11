package route

import "github.com/gin-gonic/gin"

// SetupProductRouter 设置产品路由
func SetupProductRouter(router *gin.Engine) {
	// 添加中间件
	router.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "pong",
		})
	})
}
