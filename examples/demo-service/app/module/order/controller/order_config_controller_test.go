package controller

import (
	"context"
	"testing"

	"gofy/gofy-interface-service/appinterface/demoservice/orderrequest"
)

func TestGetConfigOutput(t *testing.T) {
	c := NewOrderController(nil, nil)

	got, err := c.GetConfig(context.Background(), &orderrequest.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.PaymentTimeout != "3s" {
		t.Fatalf("payment timeout = %q, want 3s", got.PaymentTimeout)
	}
	if got.AuthRequired {
		t.Fatal("auth_required should be false in the sample output")
	}
}
