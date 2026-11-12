package container

import (
	orderservice "github.com/yourorg/demo-service/app/module/order/service"
	productservice "github.com/yourorg/demo-service/app/module/product/service"
)

// ServiceDeclare 集中声明一次装配用到的全部 service 构造槽。每个槽在首次取用
// 时按依赖链构建，之后复用；新增 service 时在这里补字段，再到对应模块的访问器
// 文件（module_product.go / module_order.go）里写构造逻辑。
type ServiceDeclare struct {
	ProductService     lazyValue[*productservice.ProductService]
	OrderConfigService lazyValue[*orderservice.OrderConfigService]
	OrderService       lazyValue[*orderservice.OrderService]
}

// NewServiceDeclare 声明本代全部构造槽，此时尚未构建任何实例。
func NewServiceDeclare() ServiceDeclare {
	return ServiceDeclare{
		ProductService:     newLazyValue[*productservice.ProductService]("product service"),
		OrderConfigService: newLazyValue[*orderservice.OrderConfigService]("order config service"),
		OrderService:       newLazyValue[*orderservice.OrderService]("order service"),
	}
}
