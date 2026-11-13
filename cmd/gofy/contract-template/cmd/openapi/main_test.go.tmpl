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
		"/api/v1/product/products",
		"bearerAuth",
		"DemoserviceOrderDTO",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("generated spec missing %q", want)
		}
	}
}
