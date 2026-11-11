package container

import (
	"testing"

	"go.uber.org/zap"
)

// 产品访问器只构建产品 service：未挂载的订单能力保持未构建。
func TestProductAccessBuildsOnlyProduct(t *testing.T) {
	c := NewContainer()
	c.Init(zap.NewNop())
	if c.ProductService() == nil {
		t.Fatal("nil product service")
	}
	if c.services.OrderService.value != nil || c.services.OrderConfigService.value != nil {
		t.Fatal("product access built order-side services")
	}
}

// 订单访问器只按依赖链解析产品 service；配置 service 由模块工厂单独取用。
func TestOrderAccessBuildsProductDependency(t *testing.T) {
	c := NewContainer()
	c.Init(zap.NewNop())
	if c.OrderService() == nil {
		t.Fatal("nil order service")
	}
	if c.services.ProductService.value == nil {
		t.Fatal("order service built without its product dependency")
	}
	if c.services.OrderConfigService.value != nil {
		t.Fatal("order accessor built the unrelated config service")
	}
}
