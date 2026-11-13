package product_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/bingcool/gofy"
	gofyadapter "github.com/bingcool/gofy-interface-api/adapter/gofy"
	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/bingcool/gofy/gofytest"
	"github.com/bingcool/gofy/support/sqlite"
	"github.com/yourorg/demo-service/app/container"
	appio "github.com/yourorg/demo-service/app/io"
	"github.com/yourorg/demo-service/app/middleware"
	"github.com/yourorg/demo-service/app/module/product"
	"github.com/yourorg/demo-service/app/module/product/model"
	productrepo "github.com/yourorg/demo-service/app/module/product/repository"
	"github.com/yourorg/interface-service/appinterface/demoservice/productdto"
	"go.uber.org/zap"
)

// newTestApp 用纯 Go SQLite 建产品表并发布为 app/io 快照，再按 demo-service 的
// 装配方式启动测试 App：产品模块绑定契约分组，应用层中间件通过
// WithMiddlewareRegistrar 注册进框架创建的共享注册表（分组声明了
// "audit"、"auth" 两个别名）。
func newTestApp(t *testing.T) *gofytest.App {
	t.Helper()
	conn, err := sqlite.New(context.Background(), sqlite.Config{
		DSN:      ":memory:",
		LogLevel: "silent",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlite.Close(conn); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	if err := conn.AutoMigrate(&model.Product{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seeds := []model.Product{
		{ID: 1, Name: "Gofy T-Shirt", Price: 9900, Stock: 120, Status: productrepo.StatusOnSale},
		{ID: 2, Name: "Retired Mug", Price: 3900, Stock: 0, Status: 0},
	}
	if err := conn.Create(&seeds).Error; err != nil {
		t.Fatalf("seed products: %v", err)
	}
	prev := appio.Current()
	appio.PublishDB(conn)
	t.Cleanup(func() { appio.Publish(prev) })

	ctr := container.NewContainer()
	ctr.Init(zap.NewNop())
	module := product.NewModule(ctr.ProductService(), zap.NewNop(), gofyadapter.BindOptions{})
	return gofytest.New(t,
		gofy.WithName("demo-service-test"),
		gofy.WithMiddlewareRegistrar(func(reg gofy.MiddlewareRegistration) error {
			return middleware.Register(reg.Registry, middleware.Deps{
				Logger:    zap.NewNop(),
				Responder: reg.Responder,
			})
		}),
		gofy.WithModules(module),
	)
}

func TestGetProductContract(t *testing.T) {
	app := newTestApp(t)
	rr := app.GET("/api/v1/product/products/1")
	gofytest.Status(t, rr, http.StatusOK)

	resp := gofytest.JSON[struct {
		Product *productdto.ProductDTO `json:"product"`
	}](t, rr)
	if resp.Product == nil {
		t.Fatal("product is nil")
	}
	p := resp.Product
	if p.ID.Uint64() != 1 || p.Name != "Gofy T-Shirt" || p.Price != 9900 || p.Stock != 120 || !p.OnSale {
		t.Fatalf("product = %+v", p)
	}
}

func TestGetProductNotFound(t *testing.T) {
	app := newTestApp(t)
	rr := app.GET("/api/v1/product/products/404")
	gofytest.Status(t, rr, http.StatusNotFound)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(60001) {
		t.Fatalf("code = %v, want 60001", code)
	}
}

func TestListProductsContract(t *testing.T) {
	app := newTestApp(t)
	rr := app.GET("/api/v1/product/products?page=1&page_size=1")
	gofytest.Status(t, rr, http.StatusOK)

	resp := gofytest.JSON[paging.ListData[productdto.ProductDTO]](t, rr)
	if resp.Total != 2 || len(resp.List) != 1 {
		t.Fatalf("list = %+v", resp)
	}
	// 仓库按 id 倒序分页，第一页第一条是最新（id 最大）的产品。
	if resp.List[0].ID.Uint64() != 2 || resp.List[0].OnSale {
		t.Fatalf("first product = %+v", resp.List[0])
	}
}
