package apprepository

import (
	"github.com/bingcool/gofy/support/registry"
	orderrepo "github.com/yourorg/demo-service/app/module/order/repository"
	productrepo "github.com/yourorg/demo-service/app/module/product/repository"
)

// OrderOrders 取订单仓库；名字缺失、实例为 nil 或类型不符时返回错误。
func OrderOrders(reg *registry.Registry) (orderrepo.OrderRepositoryInterface, error) {
	return registry.Get[orderrepo.OrderRepositoryInterface](reg, NameOrderOrders)
}

// ProductProducts 取产品仓库；名字缺失、实例为 nil 或类型不符时返回错误。
func ProductProducts(reg *registry.Registry) (productrepo.ProductRepositoryInterface, error) {
	return registry.Get[productrepo.ProductRepositoryInterface](reg, NameProductProducts)
}
