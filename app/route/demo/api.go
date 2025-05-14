package demo

import (
	"github.com/bingcool/gofy/app/middleware"
	"github.com/bingcool/gofy/app/module/demo/controller"
	"github.com/bingcool/gofy/app/route/build"
	"github.com/gin-contrib/requestid"
	"github.com/gin-gonic/gin"
)

// SetOrderRouter 设置路由
func SetOrderRouter(engine *gin.Engine, route build.RouteInterface) {
	// 添加中间件
	v1 := engine.Group("/api/v1")
	{
		// 启用 RequestID 中间件（默认生成 UUID）
		v1.Use(requestid.New())
		// 路由中间件
		v1.Use(middleware.ValidateLogin())

		// 路由处理
		v1.GET("/get-order-list", func(ctx *gin.Context) {
			request := &controller.GetOrderListRequest{}
			route.BuildReq(ctx, request)
			res, err := controller.NewOrder().GetOrderList(ctx, request)
			route.Response(ctx, res, err)
		})
	}
}
