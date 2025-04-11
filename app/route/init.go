package route

import "github.com/gin-gonic/gin"

// RegisterRouter 注册路由
func RegisterRouter(router *gin.Engine) {
	SetOrderRouter(router)
	SetupProductRouter(router)
}
