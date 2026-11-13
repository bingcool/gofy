package gofyadapter_test

import (
	"net/http"
	"strings"
	"testing"

	gofyadapter "github.com/bingcool/gofy-interface-api/adapter/gofy"
	"github.com/bingcool/gofy-interface-api/contracts/common/endpoint"
	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/middleware"
	"github.com/gin-gonic/gin"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderdto"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderroute"
)

// newOrderRouter 建一个只挂订单契约的 router，中间件把执行顺序写进 trace。
func newOrderRouter(t *testing.T, trace *[]string, opts gofyadapter.BindOptions) *gin.Engine {
	t.Helper()
	record := func(name string) support.Middleware {
		return func(c *gin.Context) {
			*trace = append(*trace, name)
			c.Next()
		}
	}
	opts.Global = append([]support.Middleware{record("global")}, opts.Global...)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// 契约分组已携带 /api/v1/order，Router 不再叠加前缀。
	router := support.NewRouter(engine, support.RouterConfig{
		Responder: support.NewResponder(support.ResponderConfig{}),
	})
	if err := gofyadapter.BindGroup(router, orderroute.Orders, orderImpl{}, opts); err != nil {
		t.Fatalf("BindGroup: %v", err)
	}
	return engine
}

// registryWith 把记录型中间件注册进一份新的注册表。
func registryWith(t *testing.T, trace *[]string, aliases map[string]string) *middleware.Registry {
	t.Helper()
	reg := middleware.NewRegistry()
	for alias, label := range aliases {
		if err := reg.Register(alias, func(c *gin.Context) {
			*trace = append(*trace, label)
			c.Next()
		}); err != nil {
			t.Fatalf("Register(%q): %v", alias, err)
		}
	}
	return reg
}

func TestBindGroupResolvesNamesFromRegistry(t *testing.T) {
	var trace []string
	reg := registryWith(t, &trace, map[string]string{"audit": "group", "auth": "auth", "order-audit": "route-name"})
	engine := newOrderRouter(t, &trace, gofyadapter.BindOptions{Registry: reg})

	rec := request(engine, http.MethodGet, "/api/v1/order/orders?user_id=10001", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rec.Code, rec.Body)
	}
	if got := strings.Join(trace, ","); got != "group,auth,global" {
		t.Fatalf("list middleware order = %q, want group,auth,global", got)
	}

	trace = nil
	rec = request(engine, http.MethodPost, "/api/v1/order/orders",
		`{"user_id":"10001","amount":1999,"items":[{"sku_id":"90001","num":2}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body)
	}
	if got := strings.Join(trace, ","); got != "group,auth,global,route-name" {
		t.Fatalf("create middleware order = %q, want group,auth,global,route-name", got)
	}
}

func TestBindOptionsMiddlewareOverridesRegistry(t *testing.T) {
	var trace []string
	reg := registryWith(t, &trace, map[string]string{"audit": "registry-group", "auth": "auth", "order-audit": "route-name"})
	engine := newOrderRouter(t, &trace, gofyadapter.BindOptions{
		Registry: reg,
		Middleware: map[string]support.Middleware{
			"audit": func(c *gin.Context) {
				trace = append(trace, "override-group")
				c.Next()
			},
		},
	})

	rec := request(engine, http.MethodGet, "/api/v1/order/orders?user_id=10001", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	if got := strings.Join(trace, ","); got != "override-group,auth,global" {
		t.Fatalf("middleware order = %q, want the explicit map to win over the registry", got)
	}

	trace = nil
	rec = request(engine, http.MethodPost, "/api/v1/order/orders",
		`{"user_id":"10001","amount":1999,"items":[{"sku_id":"90001","num":2}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body)
	}
	if got := strings.Join(trace, ","); got != "override-group,auth,global,route-name" {
		t.Fatalf("create middleware order = %q, want the unshadowed alias to come from the registry", got)
	}
}

// stubRegistry 证明 adapter 只依赖 Lookup 方法，不绑定 support/middleware.Registry。
type stubRegistry map[string]support.Middleware

func (s stubRegistry) Lookup(name string) (support.Middleware, bool) {
	mw, ok := s[name]
	return mw, ok
}

func TestBindGroupAcceptsCustomRegistry(t *testing.T) {
	var trace []string
	engine := newOrderRouter(t, &trace, gofyadapter.BindOptions{
		Registry: stubRegistry{
			"audit": func(c *gin.Context) {
				trace = append(trace, "stub-group")
				c.Next()
			},
			"auth": func(c *gin.Context) {
				trace = append(trace, "stub-auth")
				c.Next()
			},
			"order-audit": func(c *gin.Context) { c.Next() },
		},
	})

	rec := request(engine, http.MethodGet, "/api/v1/order/orders?user_id=10001", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	if got := strings.Join(trace, ","); got != "stub-group,stub-auth,global" {
		t.Fatalf("middleware order = %q, want the custom registry to be used", got)
	}
}

func TestBindGroupRejectsGroupNameMissingFromRegistry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := support.NewRouter(gin.New(), support.RouterConfig{})
	reg := middleware.NewRegistry()
	if err := reg.Register("other", func(c *gin.Context) { c.Next() }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	err := gofyadapter.BindGroup(router, orderroute.Orders, orderImpl{}, gofyadapter.BindOptions{Registry: reg})
	want := `group "/api/v1/order": middleware "audit" is not registered`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
}

// newRouterWithRegistry 建一个携带 App 级注册表的 router，模拟 gofy 启动时
// support.RouterConfig.Middleware 的装配方式。prefix 只给使用相对路径的
// 临时契约描述符补充部署前缀；orderroute 分组已自带前缀时传空。
func newRouterWithRegistry(t *testing.T, reg *middleware.Registry, prefix string) (*gin.Engine, *support.Router) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	router := support.NewRouter(engine, support.RouterConfig{
		Prefix:     prefix,
		Responder:  support.NewResponder(support.ResponderConfig{}),
		Middleware: reg,
	})
	return engine, router
}

func TestBindGroupResolvesNamesFromRouterRegistry(t *testing.T) {
	var trace []string
	reg := registryWith(t, &trace, map[string]string{"audit": "group", "auth": "auth", "order-audit": "route-name"})
	engine, router := newRouterWithRegistry(t, reg, "")

	if err := gofyadapter.BindGroup(router, orderroute.Orders, orderImpl{}, gofyadapter.BindOptions{}); err != nil {
		t.Fatalf("BindGroup: %v", err)
	}

	rec := request(engine, http.MethodGet, "/api/v1/order/orders?user_id=10001", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rec.Code, rec.Body)
	}
	if got := strings.Join(trace, ","); got != "group,auth" {
		t.Fatalf("list middleware order = %q, want group,auth", got)
	}

	trace = nil
	rec = request(engine, http.MethodPost, "/api/v1/order/orders",
		`{"user_id":"10001","amount":1999,"items":[{"sku_id":"90001","num":2}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body)
	}
	if got := strings.Join(trace, ","); got != "group,auth,route-name" {
		t.Fatalf("create middleware order = %q, want group,auth,route-name", got)
	}
}

// AuthRequired 只是 OpenAPI 标记，adapter 不会自动附加认证中间件。
func TestBindAllAuthRequiredAddsNoImplicitMiddleware(t *testing.T) {
	var trace []string
	engine, router := newRouterWithRegistry(t, middleware.NewRegistry(), "/api/v1/order")

	desc := endpoint.Get[orderrequest.ListOrdersRequest, paging.ListData[orderdto.OrderDTO]](
		"/orders", func(spec *endpoint.Spec) {
			spec.OperationID = "ListOrders"
			spec.Auth = endpoint.AuthRequired
		})
	if err := gofyadapter.BindAll(router, []endpoint.Descriptor{desc}, orderImpl{}, gofyadapter.BindOptions{}); err != nil {
		t.Fatalf("BindAll: %v", err)
	}

	rec := request(engine, http.MethodGet, "/api/v1/order/orders?user_id=10001", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	if len(trace) != 0 {
		t.Fatalf("middleware order = %q, want no implicit middleware", strings.Join(trace, ","))
	}
}

// Auth, like any other middleware, must be declared and registered by name.
// 认证与其他中间件一样，必须按名字显式声明并注册。
func TestBindAllResolvesExplicitAuthName(t *testing.T) {
	var trace []string
	reg := middleware.NewRegistry()
	reg.MustRegister("auth", func(c *gin.Context) {
		trace = append(trace, "auth")
		c.Next()
	})
	engine, router := newRouterWithRegistry(t, reg, "/api/v1/order")

	desc := endpoint.Get[orderrequest.ListOrdersRequest, paging.ListData[orderdto.OrderDTO]](
		"/orders", func(spec *endpoint.Spec) {
			spec.OperationID = "ListOrders"
			spec.Auth = endpoint.AuthRequired
			spec.Middleware = []string{"auth"}
		})
	if err := gofyadapter.BindAll(router, []endpoint.Descriptor{desc}, orderImpl{}, gofyadapter.BindOptions{}); err != nil {
		t.Fatalf("BindAll: %v", err)
	}

	rec := request(engine, http.MethodGet, "/api/v1/order/orders?user_id=10001", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	if got := strings.Join(trace, ","); got != "auth" {
		t.Fatalf("middleware order = %q, want auth resolved from the router registry", got)
	}
}

func TestBindAllRejectsRouteNameMissingFromRegistry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	desc := endpoint.Get[orderrequest.ListOrdersRequest, paging.ListData[orderdto.OrderDTO]](
		"/orders", func(spec *endpoint.Spec) {
			spec.OperationID = "ListOrders"
			spec.Middleware = []string{"absent"}
		})

	for _, tc := range []struct {
		name string
		opts gofyadapter.BindOptions
	}{
		{name: "empty registry", opts: gofyadapter.BindOptions{Registry: middleware.NewRegistry()}},
		{name: "nil registry", opts: gofyadapter.BindOptions{}},
		{
			name: "nil entry in explicit map",
			opts: gofyadapter.BindOptions{Middleware: map[string]support.Middleware{"absent": nil}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := support.NewRouter(gin.New(), support.RouterConfig{})
			err := gofyadapter.BindAll(router, []endpoint.Descriptor{desc}, orderImpl{}, tc.opts)
			want := `ListOrders: middleware "absent" is not registered`
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want it to contain %q", err, want)
			}
		})
	}
}
