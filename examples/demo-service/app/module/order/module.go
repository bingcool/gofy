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
	"github.com/bingcool/gofy/transport"
	"github.com/yourorg/demo-service/app/middleware"
	"github.com/yourorg/demo-service/app/module/order/controller"
	"github.com/yourorg/demo-service/app/module/order/repository"
	"github.com/yourorg/demo-service/app/module/order/service"
	gofyadapter "gofy/gofy-interface-service/adapter/gofy"
	"gofy/gofy-interface-service/appinterface/demoservice"
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
	svc  *service.OrderService
	auth transport.Middleware
}

// NewModuleFactory builds the module from the application runtime.
func NewModuleFactory(cfg Config) gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (transport.Module, error) {
		if rt.DB == nil {
			return nil, errors.New("order module requires database")
		}
		repo := repository.New(rt.DB)
		svc := service.New(repo, rt.Logger, service.Config{PaymentTimeout: cfg.PaymentTimeout})
		return NewModule(cfg, svc), nil
	}
}

// NewModule builds a module around an existing service. It is used by tests
// and by callers that provide their own repository.
func NewModule(cfg Config, svc *service.OrderService) *Module {
	verify := func(string) error { return nil }
	if cfg.AuthToken != "" {
		verify = func(token string) error {
			if token != cfg.AuthToken {
				return errors.New("invalid token")
			}
			return nil
		}
	}
	return &Module{svc: svc, auth: middleware.Auth(verify)}
}

// Name returns the route prefix of the module.
func (m *Module) Name() string { return "order" }

// Register binds every demoservice contract route to the handler.
func (m *Module) Register(r *transport.Router) error {
	h := controller.New(m.svc)
	return gofyadapter.BindAll(r, demoservice.Routes(), h, gofyadapter.BindOptions{
		Auth: m.auth,
	})
}
