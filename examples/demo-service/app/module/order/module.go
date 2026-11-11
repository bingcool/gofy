// Package order wires the demo order module.
//
// Layering convention:
//   - controller speaks the gofy-interface-service contract and never imports
//     model / repository / innerapi;
//   - service implements innerapi.OrderAPI, accepts plain values and returns
//     innerapi views, touching model only in its view conversion;
//   - repository owns persistence and the model type.
//
// 跨模块只依赖产品 innerapi 契约，由组合根创建的共享注册表在装配期取用。
package order

import (
	"errors"
	"fmt"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/registry"
	"github.com/yourorg/demo-service/app/module/order/controller"
	orderapi "github.com/yourorg/demo-service/app/module/order/innerapi"
	"github.com/yourorg/demo-service/app/module/order/repository"
	"github.com/yourorg/demo-service/app/module/order/service"
	productapi "github.com/yourorg/demo-service/app/module/product/innerapi"
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

// NewModuleFactory 从组合根传入的共享注册表取用产品 innerapi，构造订单 service
// 并登记订单自己的 innerapi。注册表每次装配由 reload.factories 新建，产品工厂
// 先于本工厂执行；缺登记或顺序错时构造期 Get 失败、启动即报错。
func NewModuleFactory(reg *registry.Registry) gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		if rt.DB == nil {
			return nil, errors.New("order module requires database")
		}
		products, err := registry.Get[productapi.ProductAPI](reg, productapi.Name)
		if err != nil {
			return nil, fmt.Errorf("order module: %w", err)
		}
		// 同包协作 service 先构造，再经 Dependencies 注入 OrderService（模块内
		// 组合用具体类型，不走 innerapi / 注册表）。
		cfgSvc := service.NewOrderConfigService(rt.Logger)
		svc := service.NewOrderService(service.Dependencies{
			Orders:   &repository.OrderRepository{},
			Products: products,
			Config:   cfgSvc,
			Logger:   rt.Logger,
		})
		// 登记订单自己的 innerapi，保持与产品模块对称（本轮无跨模块消费方）。
		if err := reg.Register(orderapi.Name, svc); err != nil {
			return nil, fmt.Errorf("order module: %w", err)
		}
		return NewModule(svc, rt.Logger), nil
	}
}

// NewModule builds a module around an existing service. It is used by tests
// and by callers that provide their own repository. A nil logger disables logs.
func NewModule(svc *service.OrderService, logger *zap.Logger) *Module {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Module{svc: svc, logger: logger}
}

// Name returns the route prefix of the module.
func (m *Module) Name() string { return "order" }

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
