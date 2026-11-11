package controller

import (
	"context"
	"testing"

	"github.com/yourorg/demo-service/app/module/order/service"
	"gofy/gofy-interface-service/appinterface/demoservice/orderrequest"
)

func TestGetConfigOutput(t *testing.T) {
	// GetConfig 只用同包协作的 OrderConfigService，Orders 留空即可。
	svc := service.NewOrderService(service.Dependencies{
		Config: service.NewOrderConfigService(nil),
	})
	c := NewOrderController(svc, nil)

	got, err := c.GetConfig(context.Background(), &orderrequest.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.AuthRequired {
		t.Fatal("auth_required should be false in the sample output")
	}
}
