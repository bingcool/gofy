package demoserviceclient_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bingcool/gofy-interface-api/contracts/common/types"
	"github.com/yourorg/interface-service/appinterface/demoservice/demoserviceclient"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
)

func TestNewWithBaseURLUsesFixedAddress(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"order":{"id":"7","user_id":"1","amount":1999,"status":"pending","created_at":"2026-11-05T00:00:00Z"}}}`)
	}))
	defer server.Close()

	cases := map[string]string{
		"with scheme":   server.URL,
		"host and port": strings.TrimPrefix(server.URL, "http://"),
	}
	for name, base := range cases {
		t.Run(name, func(t *testing.T) {
			orders, err := demoserviceclient.NewWithBaseURL(base)
			if err != nil {
				t.Fatalf("NewWithBaseURL: %v", err)
			}
			res, err := orders.GetOrder(context.Background(), &orderrequest.GetOrderRequest{ID: types.Uint64String(7)})
			if err != nil {
				t.Fatalf("GetOrder: %v", err)
			}
			if res.Order == nil || res.Order.ID != 7 {
				t.Fatalf("order = %+v", res.Order)
			}
			if gotPath != "/api/v1/order/orders/7" {
				t.Fatalf("path = %q", gotPath)
			}
		})
	}
}

func TestNewWithBaseURLRejectsEmpty(t *testing.T) {
	if _, err := demoserviceclient.NewWithBaseURL("  "); err == nil {
		t.Fatal("NewWithBaseURL accepted an empty address")
	}
}
