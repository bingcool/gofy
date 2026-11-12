// Package product wires the demo product module.
//
// Layering convention:
//   - innerapi is the pure contract published to other modules;
//   - service implements innerapi.ProductAPI and owns the use cases;
//   - repository owns persistence and the model type, returning model only.
//
// 与 order 模块不同，本轮产品模块不挂 HTTP 路由：能力只通过 innerapi 发布，
// 消费方是订单 service。service 由装配根（app/container）构造，并按
// innerapi.ProductAPI 接口注入订单侧；模块根只作为模块清单的一员存在，负责
// Name 与（空的）Register，模块生命周期（工厂 → Register → Start）与其它模块
// 保持一致。
package product

import (
	"github.com/bingcool/gofy/support"
	"github.com/yourorg/demo-service/app/module/product/service"
	"go.uber.org/zap"
)

// Module is the product module root.
type Module struct {
	// productService 是本次服务容器构造的产品 service，实现 innerapi.ProductAPI。
	// 模块根只持有引用；跨模块发布由装配根按接口注入，不经过模块根。
	productService *service.ProductService
	// logger 是本次服务的 logger，与 order 模块同源；当前模块根没有路由与后台任务，
	// 仅保留以便日后补产品路由时构造 controller。
	logger *zap.Logger
}

// NewModule builds a module around an existing service. A nil logger disables
// logs. 装配根或测试直接构造 service 后调用。
func NewModule(productService *service.ProductService, logger *zap.Logger) *Module {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Module{productService: productService, logger: logger}
}

// Name is the module identity used for deduplication and error messages.
func (m *Module) Name() string { return "product" }

// Prefix 返回模块挂载前缀。契约分组携带完整部署前缀，模块必须挂在根 Router
// 上；产品模块本轮没有路由，同样显式声明空前缀，避免默认的 "/product" 在日后
// 补路由时叠加到契约路径前面。
func (m *Module) Prefix() string { return "" }

// Register 本轮不绑定任何路由：产品能力只通过 innerapi 暴露给其它模块。
// 保留空实现而不是在模块清单里特判，让每个模块都走同一套工厂 → Register →
// Start 生命周期。
func (m *Module) Register(_ *support.Router) error { return nil }
