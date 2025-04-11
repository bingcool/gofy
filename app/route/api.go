package route

import (
	"github.com/bingcool/gofy/app/middleware"
	"github.com/gin-gonic/gin"
)

func SetOrderRouter(router *gin.Engine) {
	// 添加中间件
	v1 := router.Group("/api/v1")
	{
		// 路由中间件
		v1.Use(middleware.ValidateLogin())

		// 路由处理
		v1.GET("/ping", func(c *gin.Context) {
			c.JSON(200, gin.H{
				"message": "pong",
			})
		})
	}
}
