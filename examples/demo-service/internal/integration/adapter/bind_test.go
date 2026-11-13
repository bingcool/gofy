package gofyadapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gofyadapter "github.com/bingcool/gofy-adapter/adapter/gofy"
	"github.com/bingcool/gofy-interface-api/contracts/common/endpoint"
	"github.com/bingcool/gofy-interface-api/contracts/common/errcode"
	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/middleware"
	"github.com/gin-gonic/gin"
	"github.com/yourorg/interface-service/appinterface/demoservice"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderdto"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderroute"
)

type orderImpl struct{}

var (
	_ demoservice.OrdersService = (*orderImpl)(nil)
	_ demoservice.ConfigService = (*orderImpl)(nil)
)

func (orderImpl) GetOrder(_ context.Context, req *orderrequest.GetOrderRequest) (*orderresponse.GetOrderResponse, error) {
	if req.ID == 404 {
		return nil, demoservice.ErrOrderNotFound
	}
	return &orderresponse.GetOrderResponse{Order: &orderdto.OrderDTO{ID: req.ID}}, nil
}

func (orderImpl) ListOrders(_ context.Context, req *orderrequest.ListOrdersRequest) (*paging.ListData[orderdto.OrderDTO], error) {
	return &paging.ListData[orderdto.OrderDTO]{
		Total: 1,
		List:  []orderdto.OrderDTO{{ID: req.UserID}},
	}, nil
}

func (orderImpl) CreateOrder(_ context.Context, req *orderrequest.CreateOrderRequest) (*orderresponse.CreateOrderResponse, error) {
	return &orderresponse.CreateOrderResponse{Order: &orderdto.OrderDTO{ID: req.UserID}}, nil
}

func (orderImpl) CancelOrder(_ context.Context, req *orderrequest.CancelOrderRequest) error {
	if req.ID == 404 {
		return demoservice.ErrOrderNotFound
	}
	if req.ID == 409 {
		return demoservice.ErrOrderClosed
	}
	return nil
}

func (orderImpl) GetConfig(context.Context, *orderrequest.GetConfigRequest) (*orderresponse.GetConfigResponse, error) {
	return &orderresponse.GetConfigResponse{AuthRequired: true}, nil
}

func newEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.RequestID(support.DefaultRequestIDHeader))
	// 契约分组已携带 /api/v1/order，Router 不再叠加前缀；产品路由属于另一个
	// 部署前缀，由各自的处理器绑定，这里只装订单路由。
	router := support.NewRouter(engine, support.RouterConfig{
		Prefix: "/api/v1/order",
		Responder: support.NewResponder(support.ResponderConfig{
			AlwaysOK: false,
		}),
	})
	opts := gofyadapter.BindOptions{
		Middleware: map[string]support.Middleware{
			"order-audit": func(c *gin.Context) { c.Next() },
		},
	}
	if err := gofyadapter.BindAll(router, orderroute.Routes(), orderImpl{}, opts); err != nil {
		t.Fatalf("BindAll: %v", err)
	}
	return engine
}

func request(engine *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestBindAllSuccess(t *testing.T) {
	engine := newEngine(t)

	rec := request(engine, http.MethodGet, "/api/v1/order/orders/42", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if body["code"] != float64(0) {
		t.Fatalf("code = %v", body["code"])
	}
	data := body["data"].(map[string]any)
	order := data["order"].(map[string]any)
	if order["id"] != "42" {
		t.Fatalf("id = %v, want string 42", order["id"])
	}
}

func TestBindAllConfigRoute(t *testing.T) {
	engine := newEngine(t)

	rec := request(engine, http.MethodGet, "/api/v1/order/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	data := decode(t, rec)["data"].(map[string]any)
	if data["auth_required"] != true {
		t.Fatalf("auth_required = %v", data["auth_required"])
	}
}

func TestBindAllGroupMiddlewareOrder(t *testing.T) {
	var order []string
	record := func(name string) support.Middleware {
		return func(c *gin.Context) {
			order = append(order, name)
			c.Next()
		}
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// 契约分组已携带 /api/v1/order，Router 不再叠加前缀。
	router := support.NewRouter(engine, support.RouterConfig{
		Responder: support.NewResponder(support.ResponderConfig{}),
	})

	// 手动 Group 不解析契约里的分组名字，因此分组中间件（含认证）直接传实例。
	orders := router.Group(orderroute.Orders.Prefix, record("group"), record("auth"))
	err := gofyadapter.BindAll(orders, orderroute.Orders.Routes, orderImpl{}, gofyadapter.BindOptions{
		Global: []support.Middleware{record("global")},
		Middleware: map[string]support.Middleware{
			"order-audit": record("route-name"),
		},
		Routes: map[string][]support.Middleware{
			"ListOrders": {record("route")},
		},
		RateLimit: func(endpoint.Spec) support.Middleware {
			return record("rate")
		},
	})
	if err != nil {
		t.Fatalf("BindAll orders: %v", err)
	}

	config := router.Group(orderroute.Config.Prefix, record("config-group"), record("auth"))
	err = gofyadapter.BindAll(config, orderroute.Config.Routes, orderImpl{}, gofyadapter.BindOptions{
		Global: []support.Middleware{record("global")},
	})
	if err != nil {
		t.Fatalf("BindAll config: %v", err)
	}

	rec := request(engine, http.MethodGet, "/api/v1/order/orders?user_id=10001", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rec.Code, rec.Body)
	}
	// ListOrders 声明了 Spec.RateLimit，限流排在路由中间件链最前面。
	if got := strings.Join(order, ","); got != "group,auth,rate,global,route" {
		t.Fatalf("list middleware order = %q", got)
	}

	order = nil
	rec = request(engine, http.MethodGet, "/api/v1/order/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("config status = %d, body=%s", rec.Code, rec.Body)
	}
	if got := strings.Join(order, ","); got != "config-group,auth,global" {
		t.Fatalf("config middleware order = %q", got)
	}
}

func TestBindGroupMiddlewareNames(t *testing.T) {
	var order []string
	record := func(name string) support.Middleware {
		return func(c *gin.Context) {
			order = append(order, name)
			c.Next()
		}
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// 契约分组已携带 /api/v1/order，Router 不再叠加前缀。
	router := support.NewRouter(engine, support.RouterConfig{
		Responder: support.NewResponder(support.ResponderConfig{}),
	})
	// orderroute.Orders 在契约里声明了 group.Use("audit", "auth")，CreateOrder
	// 声明了 spec.Middleware = []string{"order-audit"}，两者都由显式映射解析。
	err := gofyadapter.BindGroup(router, orderroute.Orders, orderImpl{}, gofyadapter.BindOptions{
		Middleware: map[string]support.Middleware{
			"audit":       record("group"),
			"auth":        record("auth"),
			"order-audit": record("route-name"),
		},
		Global: []support.Middleware{record("global")},
		Routes: map[string][]support.Middleware{
			"ListOrders":  {record("route")},
			"CreateOrder": {record("route")},
		},
		RateLimit: func(endpoint.Spec) support.Middleware {
			return record("rate")
		},
	})
	if err != nil {
		t.Fatalf("BindGroup: %v", err)
	}

	rec := request(engine, http.MethodGet, "/api/v1/order/orders?user_id=10001", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rec.Code, rec.Body)
	}
	// ListOrders 声明了 Spec.RateLimit，限流排在路由中间件链最前面。
	if got := strings.Join(order, ","); got != "group,auth,rate,global,route" {
		t.Fatalf("list middleware order = %q", got)
	}

	order = nil
	rec = request(engine, http.MethodPost, "/api/v1/order/orders",
		`{"user_id":"10001","amount":1999,"items":[{"sku_id":"90001","num":2}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body)
	}
	// CreateOrder 也声明了 Spec.RateLimit，限流同样排在最前面。
	if got := strings.Join(order, ","); got != "group,auth,rate,global,route-name,route" {
		t.Fatalf("create middleware order = %q", got)
	}

	missing := endpoint.NewRouteGroup("/missing", func(g *endpoint.Group) { g.Use("absent") })
	err = gofyadapter.BindGroup(router, missing, orderImpl{}, gofyadapter.BindOptions{})
	if err == nil || !strings.Contains(err.Error(), `group "/missing": middleware "absent" is not registered`) {
		t.Fatalf("missing group middleware error = %v", err)
	}
}

func TestBindAllRejectsUnknownRouteMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newRoute := func(name string) endpoint.Descriptor {
		return endpoint.Get[orderrequest.ListOrdersRequest, paging.ListData[orderdto.OrderDTO]](
			"/orders", func(spec *endpoint.Spec) {
				spec.OperationID = "ListOrders"
				spec.Middleware = []string{name}
			})
	}

	for _, tc := range []struct {
		name     string
		registry map[string]support.Middleware
		want     string
	}{
		{name: "absent", want: `ListOrders: middleware "absent" is not registered`},
		{
			name:     "nil-entry",
			registry: map[string]support.Middleware{"nil-entry": nil},
			want:     `ListOrders: middleware "nil-entry" is not registered`,
		},
	} {
		router := support.NewRouter(gin.New(), support.RouterConfig{})
		err := gofyadapter.BindAll(router, []endpoint.Descriptor{newRoute(tc.name)}, orderImpl{},
			gofyadapter.BindOptions{Middleware: tc.registry})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestBindAllFaultMapping(t *testing.T) {
	engine := newEngine(t)

	rec := request(engine, http.MethodGet, "/api/v1/order/orders/404", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if body["code"] != float64(demoservice.ErrCodeOrderNotFound) {
		t.Fatalf("code = %v, want %d", body["code"], demoservice.ErrCodeOrderNotFound)
	}
	if body["msg"] != "order not found" {
		t.Fatalf("msg = %v", body["msg"])
	}
}

func TestBindAllValidationDetails(t *testing.T) {
	engine := newEngine(t)

	rec := request(engine, http.MethodPost, "/api/v1/order/orders", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if body["code"] != float64(errcode.CodeInvalidArgument) {
		t.Fatalf("code = %v", body["code"])
	}
	details, _ := body["details"].(map[string]any)
	for _, key := range []string{"user_id", "amount", "items"} {
		if _, ok := details[key]; !ok {
			t.Fatalf("details missing %s: %v", key, details)
		}
	}
}

func TestBindAllNoContent(t *testing.T) {
	engine := newEngine(t)

	rec := request(engine, http.MethodDelete, "/api/v1/order/orders/7", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("204 body = %q", rec.Body.String())
	}

	rec = request(engine, http.MethodDelete, "/api/v1/order/orders/409", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	if code := decode(t, rec)["code"]; code != float64(demoservice.ErrCodeOrderClosed) {
		t.Fatalf("code = %v", code)
	}
}

type incompleteImpl struct{}

func (incompleteImpl) GetOrder(context.Context, *orderrequest.GetOrderRequest) (*orderresponse.GetOrderResponse, error) {
	return nil, nil
}

func TestBindAllReportsEveryProblem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := support.NewRouter(gin.New(), support.RouterConfig{})
	err := gofyadapter.BindAll(router, demoservice.Routes(), incompleteImpl{}, gofyadapter.BindOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"ListOrders", "CreateOrder", "CancelOrder", "GetProduct", "ListProducts"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not mention %s: %v", want, err)
		}
	}
}

type streamProbeRequest struct{}

// 当前 Gofy Adapter 只实现 JSON 与 NoContent：SSE、Chunked、Download 必须在
// 启动期直接失败，不能静默注册成普通 JSON 路由。
func TestBindAllRejectsNonJSONResponseKinds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, kind := range []endpoint.ResponseKind{
		endpoint.KindSSE, endpoint.KindChunked, endpoint.KindDownload,
	} {
		route := endpoint.New[streamProbeRequest, endpoint.NoBody](endpoint.Spec{
			OperationID:  "StreamProbe",
			Method:       http.MethodGet,
			Path:         "/stream",
			ResponseKind: kind,
		})
		router := support.NewRouter(gin.New(), support.RouterConfig{})
		err := gofyadapter.BindAll(router, []endpoint.Descriptor{route}, orderImpl{}, gofyadapter.BindOptions{})
		if err == nil ||
			!strings.Contains(err.Error(), "is not supported by the gofy adapter") ||
			!strings.Contains(err.Error(), string(kind)) {
			t.Fatalf("%s: error = %v", kind, err)
		}
	}
}

func TestBindAllRejectsDuplicateOperationID(t *testing.T) {
	dup := endpoint.New[orderrequest.GetOrderRequest, orderresponse.GetOrderResponse](endpoint.Spec{
		OperationID: "GetOrder",
		Method:      "GET",
		Path:        "/dup/{id}",
	})
	gin.SetMode(gin.TestMode)
	router := support.NewRouter(gin.New(), support.RouterConfig{})
	routes := []endpoint.Descriptor{orderroute.GetOrder, dup}
	err := gofyadapter.BindAll(router, routes, orderImpl{}, gofyadapter.BindOptions{})
	if err == nil || !strings.Contains(err.Error(), "duplicate operation id") {
		t.Fatalf("unexpected error: %v", err)
	}
}
