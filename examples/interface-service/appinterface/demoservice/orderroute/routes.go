package orderroute

import "github.com/bingcool/gofy-interface-api/contracts/common/endpoint"

// Routes returns every order route descriptor in flat form (OpenAPI,
// demoservice.Routes). Server code binds the groups themselves
// (Orders / Config) with Gin-style middleware.
// Routes 以扁平形式返回全部订单路由描述符（供 OpenAPI 与 demoservice.Routes），
// 服务端绑定的是分组本身（Orders / Config），以便挂 Gin 风格中间件。
func Routes() []endpoint.Descriptor {
	return append(Orders.Routes, Config.Routes...)
}
