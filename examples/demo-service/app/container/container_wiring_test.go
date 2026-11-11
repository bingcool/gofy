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
	if c.orderService.value != nil || c.orderConfigService.value != nil {
		t.Fatal("product access built order-side services")
	}
}

// 订单访问器按依赖链解析产品和配置 service。
func TestOrderAccessBuildsDependencies(t *testing.T) {
	c := NewContainer()
	c.Init(zap.NewNop())
	if c.OrderService() == nil {
		t.Fatal("nil order service")
	}
	if c.productService.value == nil || c.orderConfigService.value == nil {
		t.Fatal("order service built without its dependencies")
	}
}
