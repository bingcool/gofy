package product_test

import (
	"testing"

	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
	"github.com/yourorg/demo-service/app/module/product"
)

// TestModuleName 校验模块名：Name 参与模块去重与错误信息。模块不实现
// Prefixer，由框架按默认规则挂载——产品模块没有路由，不需要挂载点。
func TestModuleName(t *testing.T) {
	module := product.NewModule(nil, nil)
	if got := module.Name(); got != "product" {
		t.Fatalf("Name() = %q, want %q", got, "product")
	}
	if _, ok := any(module).(interface{ Prefix() string }); ok {
		t.Fatal("product module must not override Prefix: order already owns the root prefix")
	}
}

// TestModuleRegistersNoRoutes 固定「本轮不挂 HTTP 路由」的约定：产品能力只通过
// innerapi 发布。Register 应成功返回且不向 gin 注册任何路由。
func TestModuleRegistersNoRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	router := support.NewRouter(engine, support.RouterConfig{})

	if err := product.NewModule(nil, nil).Register(router); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if routes := engine.Routes(); len(routes) != 0 {
		t.Fatalf("registered %d routes, want none: %+v", len(routes), routes)
	}
}
