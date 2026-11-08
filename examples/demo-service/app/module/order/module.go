// Package order wires the demo order module.
//
// Layering convention:
//   - controller speaks the gofy-interface-service contract and never imports model;
//   - service accepts plain values and returns orderdto.OrderDTO, touching model
//     only in its DTO conversion;
//   - repository owns the persistence interface and the model type.
package order

import (
	"errors"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
	"github.com/yourorg/demo-service/app/module/order/controller"
	"github.com/yourorg/demo-service/app/module/order/repository"
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

// NewModuleFactory builds the module from the application runtime.
func NewModuleFactory() gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		if rt.DB == nil {
			return nil, errors.New("order module requires database")
		}
		repo := repository.NewFromHandle(rt.Logger)
		svc := service.NewOrderService(service.Deps{
			Orders: repo,
			Logger: rt.Logger,
		})
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
// 中间件别名（分组 "audit"、CreateOrder 的 "order-audit"、AuthRequired 的
// "auth"），App 启动时已把这些实例注册进 Router 携带的共享注册表，
// BindGroup 直接按名字解析，模块不再创建或注入注册表。
func (m *Module) Register(r *support.Router) error {
	h := controller.NewOrderController(m.svc, m.logger)
	for _, group := range []*endpoint.Group{orderroute.Orders, orderroute.Config} {
		if err := gofyadapter.BindGroup(r, group, h, gofyadapter.BindOptions{}); err != nil {
			return err
		}
	}
	return nil
}
