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
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
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
	PaymentTimeout time.Duration `yaml:"payment_timeout" default:"3s"`
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
		svc := service.New(repo, rt.Logger, service.Config{PaymentTimeout: cfg.PaymentTimeout})
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

// Register binds every demoservice contract route to the handler. BindGroup
// resolves the middleware names declared in the contract (group "audit" and
// CreateOrder's "order-audit"), creates router.Group(prefix, mw...) and strips
// the group prefix from the absolute contract paths.
func (m *Module) Register(r *support.Router) error {
	h := controller.NewOrderController(m.svc, m.logger)
	opts := gofyadapter.BindOptions{
		Auth: m.auth,
		Middleware: map[string]support.Middleware{
			"audit":       middleware.Audit(m.logger, "group"),
			"order-audit": middleware.Audit(m.logger, "route"),
		},
	}
	for _, group := range []*endpoint.Group{orderroute.Orders, orderroute.Config} {
		if err := gofyadapter.BindGroup(r, group, h, opts); err != nil {
			return err
		}
	}
	return nil
}
