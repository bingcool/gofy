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

// TestAuthModesDriveSecurityRequirements checks the security semantics produced
// by the three AuthMode values: Required allows only the scheme, Optional allows
// the scheme or an anonymous request, None emits no security requirement. The
// assertions look at the parsed JSON, not at text order.
// TestAuthModesDriveSecurityRequirements 校验三种 AuthMode 生成的 security
// 语义：Required 只允许认证方案，Optional 允许认证方案或匿名，None 不生成
// 认证要求。断言针对解析后的 JSON，而不是文本顺序。
func TestAuthModesDriveSecurityRequirements(t *testing.T) {
	type authProbeRequest struct{}
	probe := func(operation string, auth endpoint.AuthMode, path string) endpoint.Descriptor {
		return endpoint.Get[authProbeRequest, orderresponse.GetOrderResponse](
			path, func(spec *endpoint.Spec) {
				spec.OperationID = operation
				spec.Auth = auth
			})
	}
	routes := []endpoint.Descriptor{
		probe("RequiredProbe", endpoint.AuthRequired, "/required"),
		probe("OptionalProbe", endpoint.AuthOptional, "/optional"),
		probe("NoneProbe", endpoint.AuthNone, "/none"),
	}
	spec, err := docgen.Build(routes, docgen.Config{})
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
	operation := func(path string) map[string]any {
		t.Helper()
		item, ok := doc["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
		if !ok {
			t.Fatalf("operation %s missing", path)
		}
		return item
	}

	required := operation("/required")["security"].([]any)
	if len(required) != 1 || !hasSecurityScheme(required[0], docgen.DefaultSecurityScheme) {
		t.Fatalf("required security = %#v", required)
	}

	optional := operation("/optional")["security"].([]any)
	if len(optional) != 2 {
		t.Fatalf("optional security = %#v, want scheme + anonymous alternative", optional)
	}
	if !hasSecurityScheme(optional[0], docgen.DefaultSecurityScheme) {
		t.Fatalf("optional first requirement = %#v", optional[0])
	}
	if anonymous, ok := optional[1].(map[string]any); !ok || len(anonymous) != 0 {
		t.Fatalf("optional second requirement = %#v, want empty object", optional[1])
	}

	if _, ok := operation("/none")["security"]; ok {
		t.Fatal("AuthNone operation has a security requirement")
	}
	if _, ok := operation("/none")["responses"].(map[string]any)["401"]; ok {
		t.Fatal("AuthNone operation documents 401")
	}
	for _, path := range []string{"/required", "/optional"} {
		if _, ok := operation(path)["responses"].(map[string]any)["401"]; !ok {
			t.Fatalf("%s does not document 401", path)
		}
	}
}

func hasSecurityScheme(requirement any, scheme string) bool {
	object, ok := requirement.(map[string]any)
	if !ok {
		return false
	}
	_, ok = object[scheme]
	return ok
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
