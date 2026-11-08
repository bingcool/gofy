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
	gofymiddleware "github.com/bingcool/gofy/support/middleware"
	"github.com/yourorg/demo-service/app/middleware"
	"github.com/yourorg/demo-service/app/module/order/controller"
	"github.com/yourorg/demo-service/app/module/order/repository"
	"github.com/yourorg/demo-service/app/module/order/service"
	"go.uber.org/zap"
	gofyadapter "gofy/gofy-interface-service/adapter/gofy"
	"gofy/gofy-interface-service/appinterface/demoservice/orderroute"
	"gofy/gofy-interface-service/contracts/common/endpoint"
)

// Config holds order module settings.
type Config struct {
	// AuthToken is required by every contract route when non-empty; empty
	// disables the check so the example runs without credentials.
	AuthToken string `yaml:"auth_token"`
}

// Module wires the order HTTP routes.
type Module struct {
	svc    *service.OrderService
	auth   support.Middleware
	logger *zap.Logger
}

// NewModuleFactory builds the module from the application runtime.
func NewModuleFactory(cfg Config) gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		if rt.DB == nil {
			return nil, errors.New("order module requires database")
		}
		repo := repository.NewFromHandle(rt.Logger)
		svc := service.NewOrderService(service.Deps{
			Orders: repo,
			Logger: rt.Logger,
		})
		return NewModule(cfg, svc, rt.Logger), nil
	}
}

// NewModule builds a module around an existing service. It is used by tests
// and by callers that provide their own repository. A nil logger disables logs.
func NewModule(cfg Config, svc *service.OrderService, logger *zap.Logger) *Module {
	if logger == nil {
		logger = zap.NewNop()
	}
	verify := func(string) error { return nil }
	if cfg.AuthToken != "" {
		verify = func(token string) error {
			if token != cfg.AuthToken {
				return errors.New("invalid token")
			}
			return nil
		}
	}
	return &Module{svc: svc, auth: middleware.Auth(verify), logger: logger}
}

// Name returns the route prefix of the module.
func (m *Module) Name() string { return "order" }

// Register binds every demoservice contract route to the handler. 契约只声明
// 中间件别名（分组 "audit"、CreateOrder 的 "order-audit"），这里把实例注册进
// 注册表并交给 BindGroup 解析：它按名字查出中间件，创建 router.Group(prefix, mw...)
// 并把分组前缀从契约的完整路径里裁掉。
func (m *Module) Register(r *support.Router) error {
	h := controller.NewOrderController(m.svc, m.logger)
	reg, err := m.newMiddlewareRegistry()
	if err != nil {
		return err
	}
	opts := gofyadapter.BindOptions{Auth: m.auth, Registry: reg}
	for _, group := range []*endpoint.Group{orderroute.Orders, orderroute.Config} {
		if err := gofyadapter.BindGroup(r, group, h, opts); err != nil {
			return err
		}
	}
	return nil
}

// newMiddlewareRegistry 每次装配都新建一份注册表：Nacos 热更新会重新调用
// Register，新旧实例互不共享，别名冲突也能在启动期直接暴露。
// AuthRequired 的路由由 BindOptions.Auth 安装鉴权，这里同时注册 "auth" 别名，
// 让契约也能按名字引用它；同一条路由不要既声明 AuthRequired 又 Use("auth")，
// 否则鉴权会执行两次。
func (m *Module) newMiddlewareRegistry() (*gofymiddleware.Registry, error) {
	reg := gofymiddleware.NewRegistry()
	entries := []struct {
		alias string
		mw    support.Middleware
	}{
		{middleware.NameAudit, middleware.Audit(m.logger, "group")},
		{middleware.NameOrderAudit, middleware.Audit(m.logger, "route")},
		{middleware.NameAuth, m.auth},
	}
	for _, e := range entries {
		if err := reg.Register(e.alias, e.mw); err != nil {
			return nil, err
		}
	}
	return reg, nil
}
