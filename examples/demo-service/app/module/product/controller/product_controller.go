package controller

import (
	"context"
	"errors"

	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/bingcool/gofy-interface-api/contracts/common/types"
	gofyadapter "github.com/bingcool/gofy-interface-gofy/adapter/gofy"
	"github.com/bingcool/gofy/apperr"
	"github.com/yourorg/demo-service/app/module/product/service"
	"github.com/yourorg/interface-service/appinterface/demoservice"
	"github.com/yourorg/interface-service/appinterface/demoservice/productdto"
	"github.com/yourorg/interface-service/appinterface/demoservice/productrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/productresponse"
	"go.uber.org/zap"
)

var _ demoservice.ProductsService = (*ProductController)(nil)

// ProductController adapts the demoservice product contract to
// ProductService. It maps contract requests to plain values and service views
// back to DTOs, and never imports model / repository / innerapi.
// ProductController 实现契约的 Products 分组：入参拆成普通值、返回值映射成 DTO，
// 不 import model、repository 或 innerapi，产品内部结构调整不会波及契约层。
type ProductController struct {
	productService *service.ProductService
	logger         *zap.Logger
}

// NewProductController builds a controller around the product service. A nil
// logger disables logs. Successful requests are left to the access log; the
// controller logs only the unexpected errors it turns into 500 responses.
// NewProductController 绑定产品用例，logger 为 nil 时静默。成功请求交给访问日志，
// controller 只记录转成 500 的意外错误，避免与访问日志重复采样。
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
// GetProduct 返回单个产品；service 的 ErrNotFound 映射成契约的 ErrProductNotFound，
// 其它失败返回 500，controller 不接触 innerapi 哨兵错误。
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
// ListProducts 返回一页产品，包含已下架商品：是否可售由响应的 OnSale 字段表达，
// 由调用方自行过滤，接口本身不做售中筛选。
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
