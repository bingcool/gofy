package demoservice

import (
	"context"

	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderdto"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
	"github.com/yourorg/interface-service/appinterface/demoservice/productdto"
	"github.com/yourorg/interface-service/appinterface/demoservice/productrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/productresponse"
)

// OrdersService is the server-side contract of the order route group
// (orderroute.Orders). A handler can implement only this interface and bind the
// Orders group on its own.
// OrdersService 是订单路由分组（orderroute.Orders）的服务端契约。处理器可以
// 只实现它，并单独绑定 Orders 分组。
type OrdersService interface {
	GetOrder(ctx context.Context, req *orderrequest.GetOrderRequest) (*orderresponse.GetOrderResponse, error)
	ListOrders(ctx context.Context, req *orderrequest.ListOrdersRequest) (*paging.ListData[orderdto.OrderDTO], error)
	CreateOrder(ctx context.Context, req *orderrequest.CreateOrderRequest) (*orderresponse.CreateOrderResponse, error)
	CancelOrder(ctx context.Context, req *orderrequest.CancelOrderRequest) error
}

// ConfigService is the server-side contract of the config route group
// (orderroute.Config). It is independent of OrdersService, so a config handler
// never has to implement order read/write methods.
// ConfigService 是配置路由分组（orderroute.Config）的服务端契约。它独立于
// OrdersService，配置处理器不需要实现订单读写方法。
type ConfigService interface {
	GetConfig(ctx context.Context, req *orderrequest.GetConfigRequest) (*orderresponse.GetConfigResponse, error)
}

// ProductsService is the server-side contract of the product route group
// (productroute.Products). The read-only product endpoints are independent of
// the order methods, so a product handler implements no order method at all.
// ProductsService 是产品路由分组（productroute.Products）的服务端契约。产品
// 只读接口独立于订单读写方法，产品处理器不需要实现任何订单方法。
type ProductsService interface {
	GetProduct(ctx context.Context, req *productrequest.GetProductRequest) (*productresponse.GetProductResponse, error)
	ListProducts(ctx context.Context, req *productrequest.ListProductsRequest) (*paging.ListData[productdto.ProductDTO], error)
}

// Service is the aggregated server-side contract: one handler can implement it
// as a whole, or the contract can be split per route group, each handler
// asserting the narrow interface above. BindGroup binds per group, so a split
// handler only has to satisfy its own narrow interface.
// Service 是聚合的服务端契约：既可由一个处理器整体实现，也可按路由分组拆成
// 多个处理器，各自断言上面的窄接口。BindGroup 按分组绑定，因此拆分后每个
// 分组只要求自己的窄接口。
type Service interface {
	OrdersService
	ConfigService
	ProductsService
}
