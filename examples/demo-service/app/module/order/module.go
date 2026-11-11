// Package order wires the demo order module.
//
// Layering convention:
//   - controller speaks the gofy-interface-service contract and never imports
//     model / repository / innerapi;
//   - service implements innerapi.OrderAPI, accepts plain values and returns
//     innerapi views, touching model only in its view conversion;
//   - repository owns persistence and the model type.
//
// 模块根只负责把已构造好的 service 绑定到路由；仓库与 service 的装配集中在
// app/container，跨模块依赖（产品 innerapi 实现）也由装配根在构造期注入。
package order

import (
	"github.com/bingcool/gofy/support"
	"github.com/yourorg/demo-service/app/module/order/controller"
	"github.com/yourorg/demo-service/app/module/order/service"
	"go.uber.org/zap"
	gofyadapter "gofy/gofy-interface-service/adapter/gofy"
	"gofy/gofy-interface-service/appinterface/demoservice/orderroute"
	"gofy/gofy-interface-service/contracts/common/endpoint"
)

// Module wires the order HTTP routes.
type Module struct {
	svc    *service.OrderService
	logger *zap.Logger
}

// NewModule builds a module around an existing service. A nil logger disables
// logs. 装配根（app/container）或测试直接构造 service 后调用。
func NewModule(svc *service.OrderService, logger *zap.Logger) *Module {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Module{svc: svc, logger: logger}
}

// Name is the module identity used for deduplication and error messages.
func (m *Module) Name() string { return "order" }

// Prefix 返回模块挂载前缀。契约分组已携带完整前缀（RoutePrefix =
// /api/v1/order），模块必须挂在根 Router 上，避免再叠加模块名或 API 前缀。
func (m *Module) Prefix() string { return "" }

// Register binds every demoservice contract route to the handler. 契约只声明
// 中间件别名（分组 "audit"、CreateOrder 的 "order-audit"、认证 "auth"），
// App 启动时已把这些实例注册进 Router 携带的共享注册表，BindGroup 直接按
// 名字解析，模块不再创建或注入注册表。
func (m *Module) Register(r *support.Router) error {
	h := controller.NewOrderController(m.svc, m.logger)
	for _, group := range []*endpoint.Group{orderroute.Orders, orderroute.Config} {
		if err := gofyadapter.BindGroup(r, group, h, gofyadapter.BindOptions{}); err != nil {
			return err
		}
	}
	return nil
}
