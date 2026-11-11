package container

import (
	orderservice "github.com/yourorg/demo-service/app/module/order/service"
)

// OrderConfigService 返回本代订单配置 service。它只依赖 logger，由订单模块根
// 直接注入 OrderConfigController，不经 innerapi。
func (c *Container) OrderConfigService() *orderservice.OrderConfigService {
	return c.orderConfigService.Get(func() *orderservice.OrderConfigService {
		return orderservice.NewOrderConfigService(c.loggerOrNop())
	})
}

// OrderService 返回本代订单 service。首次取用时按需解析跨模块的产品 service，
// 产品能力以 innerapi 接口注入。
func (c *Container) OrderService() *orderservice.OrderService {
	return c.orderService.Get(func() *orderservice.OrderService {
		return orderservice.NewOrderService(orderservice.Dependencies{
			OrderRepository: c.repos.OrdersRepo,
			ProductAPI:      c.ProductService(),
			Logger:          c.loggerOrNop(),
		})
	})
}
