package container

import (
	orderservice "github.com/yourorg/demo-service/app/module/order/service"
)

// OrderConfigService 返回本次服务的订单配置 service。它只依赖本次装配的订单配置与
// logger，由订单模块根直接注入 OrderConfigController，不经 innerapi。
//
// 配置在这里完成唯一一次翻译：service 收到的 Settings 只有它真正使用的字段，因此
// 订单模块不 import app/config，也未必要跟着无关配置段重新编译。
func (c *Container) OrderConfigService() *orderservice.OrderConfigService {
	return c.services.OrderConfigService.Get(func() *orderservice.OrderConfigService {
		order := c.orderConfig()
		return orderservice.NewOrderConfigService(orderservice.Settings{
			AuthRequired: order.AuthRequired,
		}, c.logger())
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
