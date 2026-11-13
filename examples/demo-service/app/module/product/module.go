// Package product wires the demo product module.
//
// Layering convention:
//   - innerapi is the pure contract published to other modules;
//   - service implements innerapi.ProductAPI and owns the use cases;
//   - repository owns persistence and the model type, returning model only;
//   - controller speaks the gofy-interface-service product contract.
//
// 模块根把产品 controller 绑定到契约的 Products 分组；产品能力同时通过
// innerapi 发布给订单模块。service 由装配根（app/container）构造，模块根只
// 负责路由装配。
package product

import (
	gofyadapter "github.com/bingcool/gofy-interface-service/apicore/adapter/gofy"
	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/productroute"
	"github.com/bingcool/gofy/support"
	"github.com/yourorg/demo-service/app/module/product/controller"
	"github.com/yourorg/demo-service/app/module/product/service"
	"go.uber.org/zap"
)

// Module is the product module root.
type Module struct {
	// productService 是本次服务容器构造的产品 service，实现 innerapi.ProductAPI。
	productService *service.ProductService
	// logger 是本次服务的 logger，与 order 模块同源，用于构造 controller。
	logger *zap.Logger
	// bindOptions 携带装配根提供的绑定选项（当前是限流回调），模块根原样转交
	// BindGroup；产品路由未声明限流，因此当前不影响产品分组。
	bindOptions gofyadapter.BindOptions
}

// NewModule builds a module around an existing service. A nil logger disables
// logs. 装配根或测试直接构造 service 后调用。
func NewModule(
	productService *service.ProductService,
	logger *zap.Logger,
	bindOptions gofyadapter.BindOptions,
) *Module {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Module{
		productService: productService,
		logger:         logger,
		bindOptions:    bindOptions,
	}
}

// Name is the module identity used for deduplication and error messages.
func (m *Module) Name() string { return "product" }

// Prefix 返回模块挂载前缀。契约分组已携带完整前缀（/api/v1/product），模块
// 必须挂在根 Router 上，避免再叠加模块名或 API 前缀。
func (m *Module) Prefix() string { return "" }

// Register binds the demoservice product route group to its controller. 分组
// 中间件（"audit"、"auth"）由 App 启动时注册的共享注册表按名字解析；产品
// 路由只读、未声明限流，所以 BindOptions 当前只原样传递。
func (m *Module) Register(r *support.Router) error {
	handler := controller.NewProductController(m.productService, m.logger)
	return gofyadapter.BindGroup(r, productroute.Products, handler, m.bindOptions)
}
