// Package order wires the demo order module.
//
// Layering convention:
//   - controller speaks the project contract and never imports
//     model / repository / innerapi;
//   - service implements innerapi.OrderAPI, accepts plain values and returns
//     innerapi views, touching model only in its view conversion;
//   - repository owns persistence and the model type.
//
// 模块根只负责把已构造好的 service 绑定到路由；仓库与 service 的装配集中在
// app/container，跨模块依赖（产品 innerapi 实现）也由装配根在构造期注入。
package order

import (
	gofyadapter "github.com/bingcool/gofy-interface-gofy/adapter/gofy"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderroute"

	"github.com/bingcool/gofy/support"
	"github.com/yourorg/demo-service/app/module/order/controller"
	"github.com/yourorg/demo-service/app/module/order/service"
	"go.uber.org/zap"
)

// Module wires the order HTTP routes.
// Module 是订单模块根：只把已构造好的订单与配置 service 绑定到契约路由分组，
// 仓库与跨模块依赖的装配留在 app/container。
type Module struct {
	orderService       *service.OrderService
	orderConfigService *service.OrderConfigService
	logger             *zap.Logger
	bindOptions        gofyadapter.BindOptions
}

// NewModule builds a module around existing services. A nil logger disables
// logs. 装配根（app/container）或测试直接构造 service 后调用；配置 service
// 单独传入，Config 分组只依赖它。bindOptions 携带装配根提供的绑定选项
// （当前是限流中间件回调），模块根只把它原样交给 BindGroup。
func NewModule(
	orderService *service.OrderService,
	orderConfigService *service.OrderConfigService,
	logger *zap.Logger,
	bindOptions gofyadapter.BindOptions,
) *Module {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Module{
		orderService:       orderService,
		orderConfigService: orderConfigService,
		logger:             logger,
		bindOptions:        bindOptions,
	}
}

// Name is the module identity used for deduplication and error messages.
// Name 返回模块标识，用于启动期模块去重、日志与错误信息；改名会影响去重与排障。
func (m *Module) Name() string { return "order" }

// Prefix 返回模块挂载前缀。契约分组已携带完整前缀（RoutePrefix =
// /api/v1/order），模块必须挂在根 Router 上，避免再叠加模块名或 API 前缀。
func (m *Module) Prefix() string { return "" }

// Register binds each demoservice route group to its own handler. 契约只声明
// 中间件别名（分组 "audit"、CreateOrder 的 "order-audit"、认证 "auth"），
// App 启动时已把这些实例注册进 Router 携带的共享注册表，BindGroup 直接按
// 名字解析，模块不再创建或注入注册表。限流也同样由契约驱动：
// bindOptions.RateLimit 对声明了 Spec.RateLimit 的路由生成限流中间件，
// 未声明的路由不限流。
//
// BindGroup 把一个实现绑定到分组的全部路由（按 OperationID 反射找方法），
// 所以 Orders 与 Config 必须各传自己的处理器：订单处理器没有 GetConfig，
// 配置处理器也不实现订单读写。
func (m *Module) Register(r *support.Router) error {
	orderHandler := controller.NewOrderController(m.orderService, m.logger)
	if err := gofyadapter.BindGroup(r, orderroute.Orders, orderHandler, m.bindOptions); err != nil {
		return err
	}
	configHandler := controller.NewOrderConfigController(m.orderConfigService)
	return gofyadapter.BindGroup(r, orderroute.Config, configHandler, m.bindOptions)
}
