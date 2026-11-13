package gofyadapter_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	gofyadapter "github.com/bingcool/gofy-adapter/adapter/gofy"
	"github.com/bingcool/gofy-interface-api/contracts/common/endpoint"
	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderroute"
)

// authProbeRoute 构造一条只用于认证声明校验的路由：OperationID 复用
// orderImpl.GetOrder，路径不声明占位符，因此其余绑定校验都能通过。
func authProbeRoute(auth endpoint.AuthMode, names ...string) endpoint.Descriptor {
	return endpoint.Get[orderrequest.GetOrderRequest, orderresponse.GetOrderResponse](
		"/auth-probe", func(spec *endpoint.Spec) {
			spec.OperationID = "GetOrder"
			spec.Auth = auth
			spec.Middleware = names
		})
}

var passthroughMiddleware = func(string) support.Middleware {
	return func(c *gin.Context) { c.Next() }
}

// requirementValidator 模拟应用侧策略：Required 要求 "auth"，Optional 要求
// 一个支持匿名放行的独立中间件名。
func requirementValidator(requiredName, optionalName string) gofyadapter.AuthValidator {
	return func(spec endpoint.Spec, declared []string) error {
		want := requiredName
		if spec.Auth == endpoint.AuthOptional {
			want = optionalName
		}
		for _, name := range declared {
			if name == want {
				return nil
			}
		}
		return fmt.Errorf("%s route must declare middleware %q", spec.Auth, want)
	}
}

func TestAuthValidatorSeesGroupAndRouteMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := support.NewRouter(gin.New(), support.RouterConfig{})

	type call struct {
		operation string
		auth      endpoint.AuthMode
		declared  []string
	}
	var calls []call
	err := gofyadapter.BindGroup(router, orderroute.Orders, orderImpl{}, gofyadapter.BindOptions{
		Middleware: map[string]support.Middleware{
			"audit":       passthroughMiddleware("audit"),
			"auth":        passthroughMiddleware("auth"),
			"order-audit": passthroughMiddleware("order-audit"),
		},
		AuthValidator: func(spec endpoint.Spec, declared []string) error {
			calls = append(calls, call{spec.OperationID, spec.Auth, append([]string(nil), declared...)})
			return nil
		},
	})
	if err != nil {
		t.Fatalf("BindGroup: %v", err)
	}

	got := make(map[string]call, len(calls))
	for _, item := range calls {
		got[item.operation] = item
	}
	if len(got) != len(orderroute.Orders.Routes) {
		t.Fatalf("validator calls = %d, routes = %d", len(got), len(orderroute.Orders.Routes))
	}
	if item, ok := got["GetOrder"]; !ok || strings.Join(item.declared, ",") != "audit,auth" {
		t.Fatalf("GetOrder declared = %v (present=%v)", item.declared, ok)
	}
	if item := got["CreateOrder"]; strings.Join(item.declared, ",") != "audit,auth,order-audit" {
		t.Fatalf("CreateOrder declared = %v", item.declared)
	}
	for operation, item := range got {
		if item.auth != endpoint.AuthRequired {
			t.Fatalf("%s auth = %q, want required", operation, item.auth)
		}
	}
}

func TestAuthValidatorAcceptsGroupLevelAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := support.NewRouter(gin.New(), support.RouterConfig{})
	err := gofyadapter.BindGroup(router, orderroute.Orders, orderImpl{}, gofyadapter.BindOptions{
		Middleware: map[string]support.Middleware{
			"audit":       passthroughMiddleware("audit"),
			"auth":        passthroughMiddleware("auth"),
			"order-audit": passthroughMiddleware("order-audit"),
		},
		AuthValidator: requirementValidator("auth", "optional-auth"),
	})
	if err != nil {
		t.Fatalf("BindGroup with group-level auth: %v", err)
	}
}

func TestAuthValidatorAcceptsRouteLevelAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := support.NewRouter(gin.New(), support.RouterConfig{})
	err := gofyadapter.BindAll(router, []endpoint.Descriptor{authProbeRoute(endpoint.AuthRequired, "auth")},
		orderImpl{}, gofyadapter.BindOptions{
			Middleware:    map[string]support.Middleware{"auth": passthroughMiddleware("auth")},
			AuthValidator: requirementValidator("auth", "optional-auth"),
		})
	if err != nil {
		t.Fatalf("BindAll with route-level auth: %v", err)
	}
}

func TestAuthValidatorAcceptsCustomAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := support.NewRouter(gin.New(), support.RouterConfig{})
	err := gofyadapter.BindAll(router, []endpoint.Descriptor{authProbeRoute(endpoint.AuthRequired, "custom-auth")},
		orderImpl{}, gofyadapter.BindOptions{
			Middleware:    map[string]support.Middleware{"custom-auth": passthroughMiddleware("custom-auth")},
			AuthValidator: requirementValidator("custom-auth", "optional-auth"),
		})
	if err != nil {
		t.Fatalf("BindAll with custom auth middleware: %v", err)
	}
}

func TestAuthValidatorRejectsRequiredWithoutAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := support.NewRouter(gin.New(), support.RouterConfig{})
	err := gofyadapter.BindAll(router, []endpoint.Descriptor{authProbeRoute(endpoint.AuthRequired)},
		orderImpl{}, gofyadapter.BindOptions{
			AuthValidator: requirementValidator("auth", "optional-auth"),
		})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"GetOrder", `must declare middleware "auth"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestAuthValidatorTreatsOptionalAsSeparatePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	validator := requirementValidator("auth", "optional-auth")

	router := support.NewRouter(gin.New(), support.RouterConfig{})
	err := gofyadapter.BindAll(router, []endpoint.Descriptor{authProbeRoute(endpoint.AuthOptional, "auth")},
		orderImpl{}, gofyadapter.BindOptions{
			Middleware:    map[string]support.Middleware{"auth": passthroughMiddleware("auth")},
			AuthValidator: validator,
		})
	if err == nil || !strings.Contains(err.Error(), `must declare middleware "optional-auth"`) {
		t.Fatalf("optional with required-only auth: error = %v", err)
	}

	router = support.NewRouter(gin.New(), support.RouterConfig{})
	err = gofyadapter.BindAll(router, []endpoint.Descriptor{authProbeRoute(endpoint.AuthOptional, "optional-auth")},
		orderImpl{}, gofyadapter.BindOptions{
			Middleware:    map[string]support.Middleware{"optional-auth": passthroughMiddleware("optional-auth")},
			AuthValidator: validator,
		})
	if err != nil {
		t.Fatalf("optional with optional policy: %v", err)
	}
}

func TestAuthValidatorSkipsAuthNone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	called := false
	router := support.NewRouter(gin.New(), support.RouterConfig{})
	err := gofyadapter.BindAll(router, []endpoint.Descriptor{authProbeRoute(endpoint.AuthNone)},
		orderImpl{}, gofyadapter.BindOptions{
			AuthValidator: func(endpoint.Spec, []string) error {
				called = true
				return errors.New("AuthNone must not be validated")
			},
		})
	if err != nil {
		t.Fatalf("BindAll with AuthNone: %v", err)
	}
	if called {
		t.Fatal("AuthValidator was called for AuthNone")
	}
}

func TestNilAuthValidatorKeepsExistingBehavior(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := support.NewRouter(gin.New(), support.RouterConfig{})
	err := gofyadapter.BindAll(router, []endpoint.Descriptor{authProbeRoute(endpoint.AuthRequired)},
		orderImpl{}, gofyadapter.BindOptions{})
	if err != nil {
		t.Fatalf("BindAll without AuthValidator: %v", err)
	}
}
