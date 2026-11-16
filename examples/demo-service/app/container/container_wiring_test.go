package container

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bingcool/gofy-interface-api/contracts/common/endpoint"
	appmiddleware "github.com/yourorg/demo-service/app/middleware"
	"go.uber.org/zap"
)

// 产品访问器只构建产品 service：未挂载的订单能力保持未构建。
func TestProductAccessBuildsOnlyProduct(t *testing.T) {
	c := NewContainer(nil)
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
	c := NewContainer(nil)
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
	opts := NewContainer(nil).BindOptions()
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

// 装配根同时给出认证声明校验：Required 认 "auth"，Optional 认独立的
// "optional-auth"，AuthNone 不参与校验。
func TestBindOptionsValidatesAuthDeclarations(t *testing.T) {
	opts := NewContainer(nil).BindOptions()
	if opts.AuthValidator == nil {
		t.Fatal("BindOptions returned no auth validator")
	}
	cases := []struct {
		name     string
		auth     endpoint.AuthMode
		declared []string
		wantErr  string
	}{
		{name: "required-group-auth", auth: endpoint.AuthRequired, declared: []string{"audit", appmiddleware.NameAuth}},
		{name: "required-missing", auth: endpoint.AuthRequired, declared: []string{"audit"}, wantErr: appmiddleware.NameAuth},
		{name: "optional-auth-is-not-enough", auth: endpoint.AuthOptional, declared: []string{appmiddleware.NameAuth}, wantErr: appmiddleware.NameOptionalAuth},
		{name: "optional-declared", auth: endpoint.AuthOptional, declared: []string{appmiddleware.NameOptionalAuth}},
		{name: "none-unchecked", auth: endpoint.AuthNone, declared: []string{"audit"}},
	}
	for _, tc := range cases {
		err := opts.AuthValidator(endpoint.Spec{OperationID: "Probe", Auth: tc.auth}, tc.declared)
		if tc.wantErr == "" {
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%s: error = %v, want %q", tc.name, err, tc.wantErr)
		}
	}
}
