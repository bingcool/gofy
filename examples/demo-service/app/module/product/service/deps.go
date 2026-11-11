package service

import (
	"github.com/yourorg/demo-service/app/module/product/repository"
	"go.uber.org/zap"
)

// Dependencies 显式声明产品 service 的依赖：本模块仓库必填，Logger 允许 nil。
// 产品模块没有跨模块依赖，因此这里只有自己的仓库。
type Dependencies struct {
	Products *repository.ProductRepository
	Logger   *zap.Logger
}

// NewProductService builds a ProductService. Products is required; a nil logger
// falls back to zap.NewNop().
func NewProductService(deps Dependencies) *ProductService {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ProductService{
		products: deps.Products,
		logger:   logger.With(zap.String("component", "product.service")),
	}
}
