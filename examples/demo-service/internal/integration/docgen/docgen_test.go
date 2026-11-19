package docgen_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bingcool/gofy-interface-api/contracts/common/endpoint"
	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/bingcool/gofy-interface-api/docgen"
	"github.com/yourorg/interface-service/appinterface"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderdto"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderroute"
)

func TestStableName(t *testing.T) {
	cases := map[reflect.Type]string{
		reflect.TypeOf(orderdto.OrderDTO{}):                  "DemoserviceOrderDTO",
		reflect.TypeOf(paging.ListData[orderdto.OrderDTO]{}): "CommonPagingListDataOfDemoserviceOrderDTO",
		reflect.TypeOf(paging.PageData[orderdto.OrderDTO]{}): "CommonPagingPageDataOfDemoserviceOrderDTO",
		reflect.TypeOf([]orderdto.OrderDTO{}):                "ArrayOfDemoserviceOrderDTO",
	}
	for typ, want := range cases {
		if got := docgen.StableName(typ); got != want {
			t.Fatalf("StableName(%s) = %q, want %q", typ, got, want)
		}
	}
}

func TestBuildIsDeterministicAndValid(t *testing.T) {
	cfg := docgen.Config{
		Title:   "Order API",
		Version: "1.0.0",
		Prefixes: map[string]string{
			"demoservice":         "/api/v1/order",
			"demoservice.product": "/api/v1/product",
		},
	}
	first, err := docgen.Build(appinterface.Routes(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	second, err := docgen.Build(appinterface.Routes(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	yamlFirst, err := first.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	yamlSecond, err := second.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	if string(yamlFirst) != string(yamlSecond) {
		t.Fatal("YAML output is not deterministic")
	}
	jsonFirst, _ := first.JSON()
	jsonSecond, _ := second.JSON()
	if string(jsonFirst) != string(jsonSecond) {
		t.Fatal("JSON output is not deterministic")
	}
	if err := first.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	text := string(yamlFirst)
	for _, want := range []string{
		"openapi: 3.0.3",
		"/api/v1/order/orders/{id}",
		"/api/v1/product/products/{id}",
		"/api/v1/product/products:",
		"CommonPagingListDataOfDemoserviceOrderDTO",
		"CommonErrorEnvelope",
		"operationId: CancelOrder",
		"\"204\":",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("YAML missing %q", want)
		}
	}
	// Order routes carry only the service tag, so they must land under the
	// service-level prefix and must never be swallowed by the product prefix.
	// 订单路由只带服务标签，必须落在服务级前缀下，不能被产品前缀吞掉。
	for _, unwanted := range []string{
		"/api/v1/product/orders",
		"/api/v1/order/products",
	} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("YAML contains mis-prefixed path %q", unwanted)
		}
	}
}

// TestPrefixFallsBackToServiceKey checks the fallback rule: a route carrying only
// the service tag still matches the service-name prefix when the prefix table
// holds only dotted group keys.
// TestPrefixFallsBackToServiceKey 校验兜底规则：路由只带服务标签、前缀表只有
// 带点号的分组 key 时，仍按服务名前缀匹配。
func TestPrefixFallsBackToServiceKey(t *testing.T) {
	routes := []endpoint.Descriptor{orderroute.GetOrder}
	spec, err := docgen.Build(routes, docgen.Config{
		Prefixes: map[string]string{"demoservice.orders": "/api/v2/order"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	text, err := spec.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	if !strings.Contains(string(text), "/api/v2/order/orders/{id}") {
		t.Fatalf("YAML missing fallback prefix:\n%s", text)
	}
}

func TestFilterByService(t *testing.T) {
	filtered := docgen.FilterByService(appinterface.Routes(), "demoservice")
	if len(filtered) != 7 {
		t.Fatalf("routes = %d, want 7", len(filtered))
	}
	if got := docgen.FilterByService(appinterface.Routes(), "paymentservice"); len(got) != 0 {
		t.Fatalf("payment routes = %d, want 0", len(got))
	}
}

type nestedQuery struct {
	Filter struct {
		ID int `form:"id"`
	} `form:"filter"`
}

func TestNestedQueryStructIsRejected(t *testing.T) {
	route := endpoint.New[nestedQuery, endpoint.NoBody](endpoint.Spec{
		OperationID: "Nested",
		Method:      "GET",
		Path:        "/nested",
		Tags:        []string{"demo"},
	})
	_, err := docgen.Build([]endpoint.Descriptor{route}, docgen.Config{})
	if err == nil || !strings.Contains(err.Error(), "nested struct") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type anonymousHolder struct {
	Inner struct {
		ID int `json:"id"`
	} `json:"inner"`
}

func TestAnonymousStructIsRejected(t *testing.T) {
	route := endpoint.New[anonymousHolder, anonymousHolder](endpoint.Spec{
		OperationID: "Anonymous",
		Method:      "POST",
		Path:        "/anonymous",
		Tags:        []string{"demo"},
	})
	_, err := docgen.Build([]endpoint.Descriptor{route}, docgen.Config{})
	if err == nil || !strings.Contains(err.Error(), "anonymous struct") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDuplicateOperationIsRejected(t *testing.T) {
	dup := endpoint.New[orderrequest.GetOrderRequest, orderresponse.GetOrderResponse](endpoint.Spec{
		OperationID: "GetOrder",
		Method:      "GET",
		Path:        "/duplicate/{id}",
	})
	_, err := docgen.Build([]endpoint.Descriptor{orderroute.GetOrder, dup}, docgen.Config{})
	if err == nil || !strings.Contains(err.Error(), "duplicate operation id") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestDocgenEmitsNoSecurityRequirement pins the consequence of removing auth
// from the contract: no operation carries a security requirement, and docgen
// derives no 401, because it cannot know whether the route installs an auth
// middleware. A 401 is declared in Spec.Errors like any other response. A
// configured scheme is still emitted into components, so client generators can
// see the shape even though nothing references it.
// TestDocgenEmitsNoSecurityRequirement 固定"契约不含认证"的后果：没有 operation
// 携带 security 要求，docgen 也不推导 401——它无法知道那条路由是否装了认证中间件。
// 401 与其它响应一样在 Spec.Errors 里声明。显式配置的方案仍写入 components，
// 让客户端生成器看到它的形状，尽管没有任何 operation 引用它。
func TestDocgenEmitsNoSecurityRequirement(t *testing.T) {
	type probeRequest struct{}
	probe := endpoint.Get[probeRequest, orderresponse.GetOrderResponse](
		"/probe", func(spec *endpoint.Spec) {
			spec.OperationID = "GetProbe"
		})
	spec, err := docgen.Build([]endpoint.Descriptor{probe}, docgen.Config{
		SecuritySchemes: map[string]docgen.SecurityScheme{
			"bearerAuth": {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := spec.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	raw, err := spec.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	operation, ok := doc["paths"].(map[string]any)["/probe"].(map[string]any)["get"].(map[string]any)
	if !ok {
		t.Fatal("operation /probe missing")
	}
	if _, ok := operation["security"]; ok {
		t.Fatal("operation carries a security requirement")
	}
	if _, ok := operation["responses"].(map[string]any)["401"]; ok {
		t.Fatal("operation documents a derived 401")
	}
	components, _ := doc["components"].(map[string]any)
	schemes, _ := components["securitySchemes"].(map[string]any)
	if _, ok := schemes["bearerAuth"]; !ok {
		t.Fatalf("configured scheme missing from components: %#v", components)
	}
}

func TestNilSpec(t *testing.T) {
	var spec *docgen.Spec
	if _, err := spec.YAML(); err == nil {
		t.Fatal("expected an error")
	}
	if err := spec.Validate(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := docgen.Build(nil, docgen.Config{}); err != nil {
		t.Fatalf("Build(nil): %v", err)
	}
}
