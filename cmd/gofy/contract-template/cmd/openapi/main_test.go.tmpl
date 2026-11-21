package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy-interface-api/docgen"
	"github.com/yourorg/interface-service/appinterface"
)

func TestProjectRoutesGenerateCompleteSpec(t *testing.T) {
	routes := appinterface.Routes()
	if len(routes) != 7 {
		t.Fatalf("Routes() = %d, want 7", len(routes))
	}
	spec, err := docgen.Build(routes, docgen.Config{
		Title:   "Interface Service API",
		Version: "1.0.0",
		Prefixes: map[string]string{
			"demoservice":         "/api/v1/order",
			"demoservice.product": "/api/v1/product",
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := spec.Validate(ctx); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	data, err := spec.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	for _, want := range []string{
		"/api/v1/order/orders/{id}",
		"/api/v1/order/config",
		"/api/v1/product/products",
		"DemoserviceOrderDTO",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("generated spec missing %q", want)
		}
	}
	// No operation carries a security requirement: the contract has no auth
	// field, and the group's "auth" middleware is invisible to docgen. A
	// generated requirement here would document a guarantee the document cannot
	// check.
	// 没有任何 operation 携带 security 要求：契约没有认证字段，分组上的 "auth"
	// 中间件对 docgen 不可见。这里若生成要求，等于文档承诺了一件它无法核实的事。
	if strings.Contains(string(data), "security:") {
		t.Fatal("generated spec carries a security requirement")
	}
}
