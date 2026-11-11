package controller

import (
	"context"

	"gofy/gofy-interface-service/appinterface/demoservice/orderrequest"
	"gofy/gofy-interface-service/appinterface/demoservice/orderresponse"
)

// GetConfig 返回订单模块配置。配置样例由 service 层的 OrderConfigService 拥有，
// controller 只把 service 返回的领域视图映射成契约响应，不读模块配置或 auth token。
func (c *OrderController) GetConfig(ctx context.Context, _ *orderrequest.GetConfigRequest) (*orderresponse.GetConfigResponse, error) {
	cfg := c.orderService.GetConfig(ctx)
	return &orderresponse.GetConfigResponse{AuthRequired: cfg.AuthRequired}, nil
}
