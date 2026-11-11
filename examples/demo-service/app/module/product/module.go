// Package product wires the demo product module.
//
// Layering convention:
//   - innerapi is the pure contract published to other modules;
//   - service implements innerapi.ProductAPI and owns the use cases;
//   - repository owns persistence and the model type, returning model only.
//
// 本轮产品模块不挂 HTTP 路由，只通过 innerapi 对其它模块发布读取与扣库存能力。
package product

import (
	"errors"
	"fmt"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/registry"
	"github.com/yourorg/demo-service/app/module/product/innerapi"
	"github.com/yourorg/demo-service/app/module/product/repository"
	"github.com/yourorg/demo-service/app/module/product/service"
	"go.uber.org/zap"
)

// Module wires the product module. 它没有路由，Register 为空；存在的意义是
// 装配 service 并把 innerapi 登记进共享注册表。
type Module struct {
	svc    *service.ProductService
	logger *zap.Logger
}

// NewModuleFactory 装配产品 service 并按契约名字登记自己的 innerapi。注册表
// 由组合根（reload.factories）每次装配新建并传入，属主模块自登记，消费方
// （订单）构造期按接口取用。产品工厂必须先于订单工厂执行。
func NewModuleFactory(reg *registry.Registry) gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		if rt.DB == nil {
			return nil, errors.New("product module requires database")
		}
		svc := service.NewProductService(service.Dependencies{
			Products: &repository.ProductRepository{},
			Logger:   rt.Logger,
		})
		// svc 满足 innerapi.ProductAPI；按契约名字登记，消费方按接口取用。
		if err := reg.Register(innerapi.Name, svc); err != nil {
			return nil, fmt.Errorf("product module: %w", err)
		}
		return NewModule(svc, rt.Logger), nil
	}
}

// NewModule builds a module around an existing service. A nil logger disables
// logs. 测试或自定义场景可跳过工厂直接构造。
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
