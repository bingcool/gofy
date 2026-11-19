// Package productroute defines the product endpoint descriptors. Each business
// route lives in its own file here; add new files and register their
// descriptors in Routes().
// 包 productroute 定义产品端点描述符。每个业务路由单独成文件，新增路由时同时
// 在 Routes() 中登记其描述符。
package productroute

import (
	"github.com/bingcool/gofy-interface-api/contracts/common/endpoint"
	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/yourorg/interface-service/appinterface/demoservice/productdto"
	"github.com/yourorg/interface-service/appinterface/demoservice/producterror"
	"github.com/yourorg/interface-service/appinterface/demoservice/productrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/productresponse"
)

// The variables below are assigned inside the Products group callback and kept
// as package variables so demoserviceclient, tests and docs can reuse them.
// 以下变量在 Products 分组回调中内联声明后赋值，保留包级变量供
// demoserviceclient、测试与文档复用。
var (
	GetProduct   *endpoint.Route[productrequest.GetProductRequest, productresponse.GetProductResponse]
	ListProducts *endpoint.Route[productrequest.ListProductsRequest, paging.ListData[productdto.ProductDTO]]
)

// Products is the read-only product route group: the group prefix is the full
// deployment prefix, in-group paths are relative, and the full route is group
// prefix + Spec.Path. The server binds it directly with
// gofyadapter.BindGroup(router, Products, ...).
// Products 是产品只读路由分组：分组前缀是完整部署前缀，组内路径只写相对
// 部分，完整路由 = 分组前缀 + Spec.Path。服务端直接
// gofyadapter.BindGroup(router, Products, ...) 绑定。
var Products = endpoint.NewRouteGroup("/api/v1/product", func(group *endpoint.Group) {
	// Middleware declares logical names only; at startup the server registers
	// same-named instances into the middleware registry and the adapter resolves
	// and installs them; "auth" maps to the app-level auth alias.
	// 中间件只声明逻辑名，服务端启动时把同名实例注册进中间件注册表后由
	// adapter 解析安装；"auth" 与应用层认证别名对应。
	group.Use("audit", "auth")

	// GetProduct GET /api/v1/product/products/{id}
	// GetProduct 按路径参数 id 查询单个产品，需认证；产品不存在返回 60001。
	GetProduct = endpoint.Get[productrequest.GetProductRequest, productresponse.GetProductResponse](
		"/products/{id}", func(spec *endpoint.Spec) {
			spec.OperationID = "GetProduct"
			spec.Summary = "查询产品详情"
			spec.Description = "按产品ID查询产品；产品不存在返回 60001。"
			// Two tags: ServiceTag keeps this route visible to -service demoservice,
			// and PrefixTag lets docgen select the product deployment prefix.
			// 两个标签：ServiceTag 让 -service demoservice 仍能聚合到本路由，
			// PrefixTag 让 docgen 选中产品部署前缀。
			spec.Tags = []string{ServiceTag, PrefixTag}
			spec.Errors = []endpoint.ErrorSpec{
				{Code: int(producterror.ErrCodeProductNotFound), Status: 404, Message: "product not found"},
			}
		})

	// ListProducts GET /api/v1/product/products
	// ListProducts 分页查询产品，需认证；按创建顺序倒序返回且包含下架产品。
	ListProducts = endpoint.Get[productrequest.ListProductsRequest, paging.ListData[productdto.ProductDTO]](
		"/products", func(spec *endpoint.Spec) {
			spec.OperationID = "ListProducts"
			spec.Summary = "分页查询产品"
			spec.Description = "按创建顺序倒序分页返回产品，包含下架产品。"
			spec.Tags = []string{ServiceTag, PrefixTag}
		})

	group.Add(GetProduct, ListProducts)
})
