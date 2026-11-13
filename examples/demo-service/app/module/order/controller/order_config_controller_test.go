package controller

import (
	"context"
	"testing"

	"github.com/bingcool/gofy-interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/demo-service/app/module/order/service"
)

func TestGetConfigOutput(t *testing.T) {
	c := NewOrderConfigController(service.NewOrderConfigService(nil))

	got, err := c.GetConfig(context.Background(), &orderrequest.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.AuthRequired {
		t.Fatal("auth_required should be false in the sample output")
	}
}
