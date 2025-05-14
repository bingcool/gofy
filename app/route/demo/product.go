package demo

import (
	"github.com/bingcool/gofy/app/module/demo/controller"
	"github.com/bingcool/gofy/app/route/build"
	"github.com/gin-contrib/requestid"
	"github.com/gin-gonic/gin"
)

// SetupProductRouter 设置产品路由
func SetupProductRouter(engine *gin.Engine, route build.RouteInterface) {
	// 添加中间件
	// 启用 RequestID 中间件（默认生成 UUID）
	engine.Use(requestid.New())

	// 路由处理
	v1 := engine.Group("/api/v2")
	v1.GET("/get-order-list", func(ctx *gin.Context) {
		request := &controller.GetOrderListRequest{}
		route.BuildReq(ctx, request)
		res, err := controller.NewOrder().GetOrderList(ctx, request)
		route.Response(ctx, res, err)
	})
}
