package controller

import (
	"context"
	"errors"

	"github.com/bingcool/gofy/apperr"
	"github.com/yourorg/demo-service/app/module/product/service"
	"go.uber.org/zap"
	gofyadapter "gofy/gofy-interface-service/apicore/adapter/gofy"
	"gofy/gofy-interface-service/apicore/contracts/common/paging"
	"gofy/gofy-interface-service/apicore/contracts/common/types"
	"gofy/gofy-interface-service/appinterface/demoservice"
	"gofy/gofy-interface-service/appinterface/demoservice/productdto"
	"gofy/gofy-interface-service/appinterface/demoservice/productrequest"
	"gofy/gofy-interface-service/appinterface/demoservice/productresponse"
)

var _ demoservice.ProductsService = (*ProductController)(nil)

// ProductController adapts the demoservice product contract to
// ProductService. It maps contract requests to plain values and service views
// back to DTOs, and never imports model / repository / innerapi.
type ProductController struct {
	productService *service.ProductService
	logger         *zap.Logger
}

// NewProductController builds a controller around the product service. A nil
// logger disables logs. Successful requests are left to the access log; the
// controller logs only the unexpected errors it turns into 500 responses.
func NewProductController(svc *service.ProductService, logger *zap.Logger) *ProductController {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ProductController{
		productService: svc,
		logger:         logger.With(zap.String("component", "product.controller")),
	}
}

// GetProduct returns one product.
func (c *ProductController) GetProduct(ctx context.Context, req *productrequest.GetProductRequest) (*productresponse.GetProductResponse, error) {
	p, err := c.productService.Get(ctx, req.ID.Uint64())
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			return nil, gofyadapter.Error(demoservice.ErrProductNotFound)
		}
		c.logger.Error("get product failed", zap.Uint64("product_id", req.ID.Uint64()), zap.Error(err))
		return nil, apperr.Internal("get product failed").WithCause(err)
	}
	return &productresponse.GetProductResponse{Product: toProductDTO(p.ID, p.Name, p.Price, p.Stock, p.OnSale)}, nil
}

// ListProducts returns one page of products.
func (c *ProductController) ListProducts(ctx context.Context, req *productrequest.ListProductsRequest) (*paging.ListData[productdto.ProductDTO], error) {
	items, total, err := c.productService.List(ctx, req.Page, req.PageSize)
	if err != nil {
		c.logger.Error("list products failed", zap.Error(err))
		return nil, apperr.Internal("list products failed").WithCause(err)
	}
	list := make([]productdto.ProductDTO, 0, len(items))
	for i := range items {
		list = append(list, *toProductDTO(items[i].ID, items[i].Name, items[i].Price, items[i].Stock, items[i].OnSale))
	}
	return &paging.ListData[productdto.ProductDTO]{Total: total, List: list}, nil
}

// toProductDTO 把 service 返回的领域视图映射成契约 DTO：uint64 转
// types.Uint64String，库存 int 转契约的 int32。
func toProductDTO(id uint64, name string, price int64, stock int, onSale bool) *productdto.ProductDTO {
	return &productdto.ProductDTO{
		ID:     types.Uint64String(id),
		Name:   name,
		Price:  price,
		Stock:  int32(stock),
		OnSale: onSale,
	}
}
