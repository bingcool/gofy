package orderroute

import (
	"github.com/bingcool/gofy-interface-api/contracts/common/endpoint"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
)

// GetConfig is assigned inline inside the Config group callback and kept as a
// package variable so demoserviceclient and tests can reuse it.
// GetConfig 由 Config 分组回调内联声明后赋值，保留包级变量供
// demoserviceclient 与测试复用。
var GetConfig *endpoint.Route[orderrequest.GetConfigRequest, orderresponse.GetConfigResponse]

// Config is the config route group of the order module: the group prefix is the
// same full deployment prefix RoutePrefix, in-group paths are relative
// (/config), so the full route is RoutePrefix + Spec.Path. Auth is declared
// explicitly by name on the group; AuthRequired only marks OpenAPI security.
// Config 是订单模块配置路由分组：分组前缀同样使用完整部署前缀 RoutePrefix，
// 组内路径只写相对部分（/config），完整路由 = RoutePrefix + Spec.Path。
// 认证按名字显式声明在分组上，AuthRequired 只用于 OpenAPI 标记。
var Config = endpoint.NewRouteGroup("/api/v1/order", func(group *endpoint.Group) {
	group.Use("auth")

	GetConfig = endpoint.Get[orderrequest.GetConfigRequest, orderresponse.GetConfigResponse](
		"/config", func(spec *endpoint.Spec) {
			spec.OperationID = "GetConfig"
			spec.Summary = "查询订单模块配置"
			spec.Tags = []string{ServiceTag}
			spec.Auth = endpoint.AuthRequired
		})
	group.Add(GetConfig)
})
