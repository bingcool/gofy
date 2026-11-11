// Package product wires the demo product module.
//
// Layering convention:
//   - innerapi is the pure contract published to other modules;
//   - service implements innerapi.ProductAPI and owns the use cases;
//   - repository owns persistence and the model type, returning model only.
//
// 本轮产品模块不挂 HTTP 路由，只通过 innerapi 对其它模块发布读取与扣库存能力。
// service 由装配根（app/container）构造，并通过 innerapi 接口注入消费方。
package product

import (
	"github.com/bingcool/gofy/support"
	"github.com/yourorg/demo-service/app/module/product/service"
	"go.uber.org/zap"
)

// Module wires the product module. 它没有路由，Register 为空；service 的装配
// 与发布由 app/container 负责，模块根只作为模块清单的一员存在。
type Module struct {
	svc    *service.ProductService
	logger *zap.Logger
}

// NewModule builds a module around an existing service. A nil logger disables
// logs. 装配根或测试直接构造 service 后调用。
func NewModule(svc *service.ProductService, logger *zap.Logger) *Module {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Module{svc: svc, logger: logger}
}

// Name returns the route prefix of the module.
func (m *Module) Name() string { return "product" }

// Register 本轮不绑定任何路由：产品能力只通过 innerapi 暴露给其它模块。
func (m *Module) Register(_ *support.Router) error { return nil }
