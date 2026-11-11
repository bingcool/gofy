package service

import (
	"github.com/yourorg/demo-service/app/module/order/repository"
	productapi "github.com/yourorg/demo-service/app/module/product/innerapi"
	"go.uber.org/zap"
)

// Dependencies 显式声明订单 service 的依赖：本模块仓库与跨模块产品契约必填，
// Config 是同包协作 service（模块内组合，具体类型），Logger 允许 nil。跨模块只
// 依赖产品 innerapi 接口，不接触其 service/repository/model 实现。
type Dependencies struct {
	Orders   *repository.OrderRepository
	Products productapi.ProductAPI
	Config   *OrderConfigService
	Logger   *zap.Logger
}

// NewOrderService builds an OrderService. Orders and Products are required; a
// nil logger falls back to zap.NewNop().
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
