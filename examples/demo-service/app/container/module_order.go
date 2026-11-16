package container

import (
	orderservice "github.com/yourorg/demo-service/app/module/order/service"
)

// OrderConfigService 返回本次服务的订单配置 service。它只依赖本次装配的订单配置与
// logger，由订单模块根直接注入 OrderConfigController，不经 innerapi。
//
// 容器只交出本代捕获的 order 段，不做翻译：service 的构造参数就是 config.OrderConfig，
// 因此配置改名或拆段时改动落在配置类型与这里一行，业务包里没有第二份同形状的结构体。
func (c *Container) OrderConfigService() *orderservice.OrderConfigService {
	return c.services.OrderConfigService.Get(func() *orderservice.OrderConfigService {
		return orderservice.NewOrderConfigService(c.orderConfig(), c.logger())
	})
}

// OrderService 返回本次服务的订单 service。首次取用时按需解析跨模块的产品 service，
// 产品能力以 innerapi 接口注入。
func (c *Container) OrderService() *orderservice.OrderService {
	return c.services.OrderService.Get(func() *orderservice.OrderService {
		return orderservice.NewOrderService(orderservice.Dependencies{
			OrderRepository: c.repos.OrdersRepo,
			ProductAPI:      c.ProductService(),
			Logger:          c.logger(),
		})
	})
}
