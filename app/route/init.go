package route

import (
	"github.com/bingcool/gofy/app/route/build"
	"github.com/bingcool/gofy/app/route/demo"
	"github.com/gin-gonic/gin"
)

// RegisterRouter 注册路由
func RegisterRouter(engine *gin.Engine) {
	demo.SetOrderRouter(engine, &build.Route{})
	demo.SetupProductRouter(engine, &build.Route{})
}
