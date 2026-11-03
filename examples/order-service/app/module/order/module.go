package order

import (
	"errors"
	"net/http"
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/transport"
	"github.com/yourorg/order-service/app/middleware"
	"github.com/yourorg/order-service/app/module/order/handler"
	"github.com/yourorg/order-service/app/module/order/repository"
	"github.com/yourorg/order-service/app/module/order/service"
)

// Config holds order module settings.
type Config struct {
	PaymentTimeout time.Duration `yaml:"payment_timeout" default:"3s"`
	// AuthToken enables bearer-token auth on DELETE when non-empty.
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
	var auth transport.Middleware
	if cfg.AuthToken != "" {
		auth = middleware.Auth(func(token string) error {
			if token != cfg.AuthToken {
				return errors.New("invalid token")
			}
			return nil
		})
	}
	return &Module{svc: svc, auth: auth}
}

func (m *Module) Name() string { return "order" }

func (m *Module) Register(r *transport.Router) error {
	h := handler.New(m.svc)
	transport.GET(r, "/orders/:id", h.Get)
	transport.POST(r, "/orders", h.Create)
	transport.GET(r, "/orders", h.List)
	if m.auth != nil {
		transport.NoContent(r, http.MethodDelete, "/orders/:id", h.Cancel, m.auth)
	} else {
		transport.NoContent(r, http.MethodDelete, "/orders/:id", h.Cancel)
	}
	return nil
}
