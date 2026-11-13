package container

import (
	productservice "github.com/yourorg/demo-service/app/module/product/service"
)

// ProductService 返回本次服务的产品 service。它只依赖产品仓库，首次取用时构造，
// 不会牵连订单侧的服务。
func (c *Container) ProductService() *productservice.ProductService {
	return c.services.ProductService.Get(func() *productservice.ProductService {
		return productservice.NewProductService(productservice.Dependencies{
			ProductRepository: c.repos.ProductsRepo,
			Logger:            c.logger(),
		})
	})
}
