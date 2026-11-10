// Package order wires the demo order module.
//
// Layering convention:
//   - controller speaks the gofy-interface-service contract and never imports model;
//   - service accepts plain values and returns orderdto.OrderDTO, touching model
//     only in its DTO conversion;
//   - repository owns persistence and the model type.
package order

import (
	"errors"
	"fmt"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/registry"
	"github.com/yourorg/demo-service/app/module/order/controller"
	"github.com/yourorg/demo-service/app/module/order/service"
	productrepo "github.com/yourorg/demo-service/app/module/product/repository"
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

// NewModuleFactory 每次装配新建一份注册表，只登记订单服务跨模块读取的产品
// 仓库；本模块仓库由 service.Deps 直接注入，不经过注册表。启动和每次 Reload
// 都重新执行工厂，新旧两代注册表互不影响。
func NewModuleFactory() gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		if rt.DB == nil {
			return nil, errors.New("order module requires database")
		}
		reg := registry.NewRegistry()
		// 产品仓库的名字和实现归产品模块，订单模块作为消费方负责登记。
		if err := reg.Register(productrepo.NameProducts, &productrepo.ProductRepository{}); err != nil {
			return nil, fmt.Errorf("order module: %w", err)
		}
		svc := service.NewOrderService(service.Deps.Orders, reg, rt.Logger)
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
