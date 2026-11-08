package middleware_test

import (
	"testing"

	"github.com/bingcool/gofy/support"
	gofymiddleware "github.com/bingcool/gofy/support/middleware"
	"github.com/gin-gonic/gin"
	"github.com/yourorg/demo-service/app/middleware"
	"go.uber.org/zap"
)

// 应用层所有别名都在 middleware.Register 里注册，App 把它们装进共享注册表。
func TestRegisterRegistersEveryAppAlias(t *testing.T) {
	reg := gofymiddleware.NewRegistry()
	if err := middleware.Register(reg, middleware.Deps{
		Logger:    zap.NewNop(),
		Responder: support.NewResponder(support.ResponderConfig{}),
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	aliases := []string{middleware.NameAuth, middleware.NameAudit, middleware.NameOrderAudit}
	for _, alias := range aliases {
		if _, ok := reg.Lookup(alias); !ok {
			t.Fatalf("alias %q is not registered", alias)
		}
	}
}

// 每次装配都由 gofy 传入一份新注册表，往旧实例加别名不会影响下一次装配。
func TestRegisterUsesTheGivenRegistry(t *testing.T) {
	first := gofymiddleware.NewRegistry()
	second := gofymiddleware.NewRegistry()
	deps := middleware.Deps{Responder: support.NewResponder(support.ResponderConfig{})}
	if err := middleware.Register(first, deps); err != nil {
		t.Fatalf("Register first: %v", err)
	}
	if err := middleware.Register(second, deps); err != nil {
		t.Fatalf("Register second: %v", err)
	}
	if err := first.Register("extra", func(c *gin.Context) { c.Next() }); err != nil {
		t.Fatalf("Register extra: %v", err)
	}
	if _, ok := second.Lookup("extra"); ok {
		t.Fatal("second registry shares aliases with the first one")
	}
}
