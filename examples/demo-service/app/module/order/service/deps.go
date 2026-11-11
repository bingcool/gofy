package service

import (
	"github.com/yourorg/demo-service/app/module/order/repository"
	productapi "github.com/yourorg/demo-service/app/module/product/innerapi"
	"go.uber.org/zap"
)

// Dependencies 显式声明订单 service 的全部依赖：本模块仓库、跨模块产品契约、
// 同包协作的 OrderConfigService 必填，Logger 允许 nil。产品能力以
// innerapi.ProductAPI 接口注入，实现由装配根提供；service 不 import 其它模块
// 的 service/repository/model。
type Dependencies struct {
	Orders   *repository.OrderRepository
	Products productapi.ProductAPI
	Config   *OrderConfigService
	Logger   *zap.Logger
}

// NewOrderService builds an OrderService from explicit dependencies. Orders,
// Products and Config are required; a nil logger falls back to zap.NewNop().
func NewOrderService(deps Dependencies) *OrderService {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OrderService{
		orders:   deps.Orders,
		products: deps.Products,
		config:   deps.Config,
		logger:   logger.With(zap.String("component", "order.service")),
	}
}
