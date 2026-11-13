// Package orderroute defines the order endpoint descriptors. Each business
// route lives in its own file here; add new files and register their
// descriptors in Routes().
// 包 orderroute 定义订单端点描述符。每个业务路由单独成文件，新增路由时同时
// 在 Routes() 中登记其描述符。
package orderroute

import (
	"github.com/bingcool/gofy-interface-api/contracts/common/endpoint"
	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderdto"
	"github.com/yourorg/interface-service/appinterface/demoservice/ordererror"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
)

// The variables below are assigned inside the Orders group callback and kept as
// package variables so demoserviceclient, tests and docs can reuse them.
// 以下变量在 Orders 分组回调中内联声明后赋值，保留包级变量供
// demoserviceclient、测试与文档复用。
var (
	GetOrder    *endpoint.Route[orderrequest.GetOrderRequest, orderresponse.GetOrderResponse]
	ListOrders  *endpoint.Route[orderrequest.ListOrdersRequest, paging.ListData[orderdto.OrderDTO]]
	CreateOrder *endpoint.Route[orderrequest.CreateOrderRequest, orderresponse.CreateOrderResponse]
	CancelOrder *endpoint.Route[orderrequest.CancelOrderRequest, endpoint.NoBody]
)

// Orders is the order read/write route group: the group prefix is the full
// deployment prefix RoutePrefix, in-group paths are relative, and the full route
// is RoutePrefix + Spec.Path. The server binds it directly with
// gofyadapter.BindGroup(router, Orders, ...).
// Orders 是订单读写路由分组：分组前缀是完整部署前缀 RoutePrefix，
// 组内路径只写相对部分，完整路由 = RoutePrefix + Spec.Path。
// 服务端直接 gofyadapter.BindGroup(router, Orders, ...) 绑定。
var Orders = endpoint.NewRouteGroup("/api/v1/order", func(group *endpoint.Group) {
	// Group middleware declares logical names only; at startup the server
	// registers same-named instances into the middleware registry
	// (support/middleware.Registry) and the adapter resolves and installs them in
	// order. Auth goes through the same path: "auth" is the app-level auth alias,
	// spec.Auth only marks OpenAPI security, and the adapter adds no middleware
	// just because AuthRequired is set.
	// 分组中间件只声明逻辑名，服务端启动时把同名实例注册进中间件注册表
	// （support/middleware.Registry），adapter 按顺序解析后安装。认证也走这里：
	// "auth" 是应用层注册的认证别名，spec.Auth 仅用于 OpenAPI 的 security 标记，
	// adapter 不会因为 AuthRequired 自动补任何中间件。
	group.Use("audit", "auth")

	// GetOrder GET /api/v1/order/orders/{id}
	// GetOrder 按路径参数 id 查询单个订单，需认证并按用户限流。
	GetOrder = endpoint.Get[orderrequest.GetOrderRequest, orderresponse.GetOrderResponse](
		"/orders/{id}", func(spec *endpoint.Spec) {
			spec.OperationID = "GetOrder"
			spec.Summary = "查询订单详情"
			spec.Description = "按订单ID查询订单；订单不存在返回 40401。"
			spec.Tags = []string{ServiceTag}
			spec.Auth = endpoint.AuthRequired
			// The contract declares the limit and the relay enforces exactly that:
			// Limit and WindowSeconds are the effective thresholds, Key decides the
			// counted subject, and the server builds the rate limit middleware from
			// them (see demo-service app/middleware.RateLimiter). RedisAlias names
			// the Redis instance used for counting, mapping to redis.ratelimit in
			// the server config.yaml; when empty the server defaults to "ratelimit".
			// 契约声明多少就限多少：Limit / WindowSeconds 是生效阈值，
			// Key 决定计数主体，服务端据此生成限流中间件（见 demo-service
			// 的 app/middleware.RateLimiter）。RedisAlias 指定计数使用的
			// Redis 命名实例，对应服务端 config.yaml 的 redis.ratelimit；
			// 为空时服务端默认 "ratelimit"。
			spec.RateLimit = &endpoint.RateLimit{Limit: 120, WindowSeconds: 60, Key: endpoint.RateKeyUser, RedisAlias: "ratelimit"}
			spec.Errors = []endpoint.ErrorSpec{
				{Code: int(ordererror.ErrCodeOrderNotFound), Status: 404, Message: "order not found"},
			}
		})

	// ListOrders GET /api/v1/order/orders
	// ListOrders 分页查询订单，分页参数走 query，需认证并按用户限流。
	ListOrders = endpoint.Get[orderrequest.ListOrdersRequest, paging.ListData[orderdto.OrderDTO]](
		"/orders", func(spec *endpoint.Spec) {
			spec.OperationID = "ListOrders"
			spec.Summary = "分页查询订单"
			spec.Tags = []string{ServiceTag}
			spec.Auth = endpoint.AuthRequired
			spec.RateLimit = &endpoint.RateLimit{Limit: 100, WindowSeconds: 60, Key: endpoint.RateKeyUser, RedisAlias: "ratelimit"}
		})

	// CreateOrder POST /api/v1/order/orders
	// 路由级中间件同样只声明名字，写在分组中间件之后执行；也可以链式声明：
	// endpoint.Post[Req, Res](path, build).Use("order-audit")。
	CreateOrder = endpoint.Post[orderrequest.CreateOrderRequest, orderresponse.CreateOrderResponse](
		"/orders", func(spec *endpoint.Spec) {
			spec.Middleware = []string{"order-audit"}
			spec.OperationID = "CreateOrder"
			spec.Summary = "创建订单"
			spec.Tags = []string{ServiceTag}
			spec.Auth = endpoint.AuthRequired
			spec.RateLimit = &endpoint.RateLimit{Limit: 60, WindowSeconds: 60, Key: endpoint.RateKeyUser, RedisAlias: "ratelimit"}
			spec.Errors = []endpoint.ErrorSpec{
				{Code: 40001, Status: 400, Message: "invalid argument"},
			}
		})

	// CancelOrder DELETE /api/v1/order/orders/{id}
	// CancelOrder 取消订单，成功返回 204 且无响应体；订单不存在 404、已关闭 409。
	CancelOrder = endpoint.Delete[orderrequest.CancelOrderRequest, endpoint.NoBody](
		"/orders/{id}", func(spec *endpoint.Spec) {
			spec.OperationID = "CancelOrder"
			spec.Summary = "取消订单"
			spec.Tags = []string{ServiceTag}
			spec.Auth = endpoint.AuthRequired
			spec.NoContent = true
			spec.RateLimit = &endpoint.RateLimit{Limit: 60, WindowSeconds: 60, Key: endpoint.RateKeyUser, RedisAlias: "ratelimit"}
			spec.Errors = []endpoint.ErrorSpec{
				{Code: int(ordererror.ErrCodeOrderNotFound), Status: 404, Message: "order not found"},
				{Code: int(ordererror.ErrCodeOrderClosed), Status: 409, Message: "order already closed"},
			}
		})

	group.Add(GetOrder, ListOrders, CreateOrder, CancelOrder)
})
