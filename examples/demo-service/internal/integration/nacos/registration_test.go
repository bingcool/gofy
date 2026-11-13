package nacos_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	gofynacos "github.com/bingcool/gofy/support/nacos"

	"github.com/bingcool/gofy-interface-api/contracts/common/types"
	nacos "github.com/bingcool/gofy-interface-gofy/client/nacos"
	"github.com/yourorg/interface-service/appinterface/demoservice"
	"github.com/yourorg/interface-service/appinterface/demoservice/demoserviceclient"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
)

// TestRegistrarAndResolverEndToEnd wires both sides of the feature: gofy's
// registrar publishes demo-service into a fake Nacos, the resolver discovers
// it, the typed client calls the discovered address, and deregistration makes
// the service undiscoverable again.
// TestRegistrarAndResolverEndToEnd 端到端串起注册与发现两侧：gofy 的注册器
// 把 demo-service 写入假 Nacos，resolver 解析出地址，类型化客户端按地址调用，
// 最后注销使服务重新不可发现。
func TestRegistrarAndResolverEndToEnd(t *testing.T) {
	fake := &fakeNaming{}
	nacosServer := httptest.NewServer(fake)
	defer nacosServer.Close()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orders/7" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"order":{"id":"7","user_id":"1","amount":1999,"status":"pending","created_at":"2026-11-05T00:00:00Z"}}}`)
	}))
	defer backend.Close()
	backendPort := backend.Listener.Addr().(*net.TCPAddr).Port

	registrar, err := gofynacos.NewRegistrar(gofynacos.Config{
		Addr:      nacosServer.URL,
		Namespace: "dev",
		Register: gofynacos.RegisterConfig{
			Enabled:     true,
			ServiceName: demoservice.ServiceName,
			IP:          "127.0.0.1",
			Port:        backendPort,
			Group:       "gofy-group",
		},
	}, nil)
	if err != nil {
		t.Fatalf("NewRegistrar: %v", err)
	}

	ctx := context.Background()
	if err := registrar.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = registrar.Stop(ctx) }()

	// A fresh resolver must see the instance the registrar published.
	// 新建的 resolver 必须能发现注册器刚刚发布的实例，否则说明注册与发现未打通。
	resolver, err := nacos.New(nacos.Config{
		Addr:      nacosServer.URL,
		Namespace: "dev",
		Group:     "gofy-group",
		CacheTTL:  time.Minute,
	})
	if err != nil {
		t.Fatalf("New resolver: %v", err)
	}
	base, err := resolver.Resolve(ctx, demoservice.ServiceName)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := "http://127.0.0.1:" + strconv.Itoa(backendPort); base != want {
		t.Fatalf("base = %q, want %q", base, want)
	}

	orders, err := demoserviceclient.NewWithResolver(resolver)
	if err != nil {
		t.Fatalf("NewWithResolver: %v", err)
	}
	res, err := orders.GetOrder(ctx, &orderrequest.GetOrderRequest{ID: types.Uint64String(7)})
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if res.Order == nil || res.Order.ID != 7 || res.Order.Amount != 1999 {
		t.Fatalf("order = %+v", res.Order)
	}

	if err := registrar.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	fresh, err := nacos.New(nacos.Config{
		Addr:      nacosServer.URL,
		Namespace: "dev",
		Group:     "gofy-group",
		CacheTTL:  time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New fresh resolver: %v", err)
	}
	if _, err := fresh.Resolve(ctx, demoservice.ServiceName); !errors.Is(err, nacos.ErrNoInstances) {
		t.Fatalf("err after deregistration = %v, want nacos.ErrNoInstances", err)
	}
}
