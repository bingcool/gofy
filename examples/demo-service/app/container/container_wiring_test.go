package container

import (
	"net/http"
	"testing"

	"go.uber.org/zap"
	"gofy/gofy-interface-service/apicore/contracts/common/endpoint"
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

// 装配根给出按契约生成限流中间件的回调：契约声明了 RateLimit 的路由拿到实例，
// 没有声明的路由（如 GetConfig）不挂限流。
func TestBindOptionsRateLimitFollowsContract(t *testing.T) {
	opts := NewContainer().BindOptions()
	if opts.RateLimit == nil {
		t.Fatal("BindOptions returned no rate limit callback")
	}
	declared := endpoint.Spec{
		OperationID: "ListOrders",
		Method:      http.MethodGet,
		RateLimit:   &endpoint.RateLimit{Limit: 100, WindowSeconds: 60, Key: endpoint.RateKeyUser},
	}
	if opts.RateLimit(declared) == nil {
		t.Fatal("declared rate limit produced no middleware")
	}
	if mw := opts.RateLimit(endpoint.Spec{OperationID: "GetConfig", Method: http.MethodGet}); mw != nil {
		t.Fatal("route without RateLimit got a limiter")
	}
}
