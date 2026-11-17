package demoserviceclient_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bingcool/gofy-interface-api/contracts/common/types"
	"github.com/yourorg/interface-service/appinterface/demoservice/demoserviceclient"
	"github.com/yourorg/interface-service/appinterface/demoservice/productrequest"
)

func TestGetProductUsesProductRoute(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"product":{"id":"90001","name":"Gofy T-Shirt","price":9900,"stock":120,"on_sale":true}}}`)
	}))
	defer server.Close()

	c, err := demoserviceclient.NewWithBaseURL(server.URL)
	if err != nil {
		t.Fatalf("NewWithBaseURL: %v", err)
	}
	res, err := c.GetProduct(context.Background(), &productrequest.GetProductRequest{ID: types.Uint64String(90001)})
	if err != nil {
		t.Fatalf("GetProduct: %v", err)
	}
	if res.Product == nil || res.Product.ID.Uint64() != 90001 || !res.Product.OnSale {
		t.Fatalf("product = %+v", res.Product)
	}
	if gotPath != "/api/v1/product/products/90001" {
		t.Fatalf("path = %q", gotPath)
	}
}
