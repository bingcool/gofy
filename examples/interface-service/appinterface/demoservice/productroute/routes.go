package productroute

import "github.com/bingcool/gofy-interface-api/contracts/common/endpoint"

// Routes returns every product route descriptor in flat form (OpenAPI,
// demoservice.Routes). Server code binds the group itself with Gin-style
// middleware.
// Routes 以扁平形式返回全部产品路由描述符（供 OpenAPI 与 demoservice.Routes），
// 服务端绑定的是分组本身，以便挂 Gin 风格中间件。
func Routes() []endpoint.Descriptor {
	return append([]endpoint.Descriptor(nil), Products.Routes...)
}
