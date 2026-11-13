package demoserviceclient

import (
	"context"

	"github.com/bingcool/gofy-interface-api/client"
	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/yourorg/interface-service/appinterface/demoservice"
	"github.com/yourorg/interface-service/appinterface/demoservice/productdto"
	"github.com/yourorg/interface-service/appinterface/demoservice/productrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/productresponse"
	"github.com/yourorg/interface-service/appinterface/demoservice/productroute"
)

// GetProduct fetches one product.
// GetProduct 按产品 ID 查询单个产品，产品不存在时返回 60001。
func (c *Client) GetProduct(ctx context.Context, req *productrequest.GetProductRequest, opts ...client.CallOption) (*productresponse.GetProductResponse, error) {
	return client.Call(ctx, c.c, productroute.GetProduct, req, opts...)
}

// ListProducts pages through products.
// ListProducts 分页查询产品，结果包含已下架产品，调用方按 on_sale 字段过滤。
func (c *Client) ListProducts(ctx context.Context, req *productrequest.ListProductsRequest, opts ...client.CallOption) (*paging.ListData[productdto.ProductDTO], error) {
	return client.Call(ctx, c.c, productroute.ListProducts, req, opts...)
}

// IsProductNotFound reports whether err carries the product-not-found code.
// IsProductNotFound 判断 err 是否携带产品不存在错误码（60001）。
func IsProductNotFound(err error) bool {
	return client.CodeOf(err) == int(demoservice.ErrCodeProductNotFound)
}
