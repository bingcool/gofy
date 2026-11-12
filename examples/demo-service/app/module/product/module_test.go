package product_test

import (
	"testing"

	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
	"github.com/yourorg/demo-service/app/module/product"
)

// TestModuleIdentity 校验模块身份：Name 参与模块去重与错误信息，Prefix 为空
// 表示挂在根 Router 上，与契约自带的完整部署前缀配合。
func TestModuleIdentity(t *testing.T) {
	module := product.NewModule(nil, nil)
	if got := module.Name(); got != "product" {
		t.Fatalf("Name() = %q, want %q", got, "product")
	}
	if got := module.Prefix(); got != "" {
		t.Fatalf("Prefix() = %q, want empty", got)
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
