package route

import (
	"github.com/bingcool/gofy/app/middleware"
	"github.com/bingcool/gofy/app/module/demo/controller"
	"github.com/gin-contrib/requestid"
	"github.com/gin-gonic/gin"
)

func SetOrderRouter(router *gin.Engine) {
	// 添加中间件
	v1 := router.Group("/api/v1")
	{
		// 启用 RequestID 中间件（默认生成 UUID）
		v1.Use(requestid.New())
		// 路由中间件
		v1.Use(middleware.ValidateLogin())

		// 路由处理
		v1.GET("/get-order-list", func(ctx *gin.Context) {
			request := &controller.GetOrderListRequest{}
			buildReq(ctx, request)
			res, err := controller.NewOrder().GetOrderList(ctx, request)
			response(ctx, res, err)
		})
	}
}
