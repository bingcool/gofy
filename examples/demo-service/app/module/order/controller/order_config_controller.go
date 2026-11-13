package controller

import (
	"context"

	"github.com/bingcool/gofy-interface-service/appinterface/demoservice"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderrequest"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderresponse"
	"github.com/yourorg/demo-service/app/module/order/service"
)

var _ demoservice.ConfigService = (*OrderConfigController)(nil)

// OrderConfigController adapts the config route group to OrderConfigService.
// 它只依赖配置 service，不持有订单读写能力，因此 Config 分组可以独立绑定。
type OrderConfigController struct {
	configService *service.OrderConfigService
}

// NewOrderConfigController builds a controller around the config service.
// NewOrderConfigController 用配置 service 构造 controller；Config 分组只绑定它，
// 不与订单读写处理器共享实例，保证两组路由各自独立。
func NewOrderConfigController(svc *service.OrderConfigService) *OrderConfigController {
	return &OrderConfigController{configService: svc}
}

// GetConfig 返回订单模块配置。配置样例由 service 层的 OrderConfigService 拥有，
// controller 只把 service 返回的领域视图映射成契约响应，不读模块配置或 auth token。
func (c *OrderConfigController) GetConfig(ctx context.Context, _ *orderrequest.GetConfigRequest) (*orderresponse.GetConfigResponse, error) {
	cfg := c.configService.GetConfig(ctx)
	return &orderresponse.GetConfigResponse{AuthRequired: cfg.AuthRequired}, nil
}
