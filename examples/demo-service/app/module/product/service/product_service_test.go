package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bingcool/gofy/support/sqlite"
	appdb "github.com/yourorg/demo-service/app/io/db"
	productapi "github.com/yourorg/demo-service/app/module/product/innerapi"
	productmodel "github.com/yourorg/demo-service/app/module/product/model"
	productrepo "github.com/yourorg/demo-service/app/module/product/repository"
	"github.com/yourorg/demo-service/app/module/product/service"
	"go.uber.org/zap"
)

// newProductService 用纯 Go 内存 SQLite 建真实仓库，把连接发布成 app/io 句柄，
// 不连外部 MySQL；仓库本身不持连接，因此每个用例结束后恢复句柄即可。
func newProductService(t *testing.T) *service.ProductService {
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
	if err := conn.AutoMigrate(&productmodel.Product{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	products := []productmodel.Product{
		{ID: 1, Name: "Gofy T-Shirt", Price: 9900, Stock: 10, Status: productrepo.StatusOnSale},
		{ID: 2, Name: "Gofy Mug", Price: 3900, Stock: 5, Status: 0},
	}
	if err := conn.Create(&products).Error; err != nil {
		t.Fatalf("seed products: %v", err)
	}
	prev := appdb.Db
	appdb.Db = conn
	t.Cleanup(func() { appdb.Db = prev })
	return service.NewProductService(service.Dependencies{
		Products: &productrepo.ProductRepository{},
		Logger:   zap.NewNop(),
	})
}

func TestGetReturnsViewOrNotFound(t *testing.T) {
	svc := newProductService(t)
	ctx := context.Background()

	view, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get(1): %v", err)
	}
	if view.Name != "Gofy T-Shirt" || !view.OnSale {
		t.Fatalf("unexpected product view: %+v", view)
	}

	if _, err := svc.Get(ctx, 404); !errors.Is(err, productapi.ErrNotFound) {
		t.Fatalf("Get(404) error = %v, want ErrNotFound", err)
	}
}

func TestListByIDsSkipsMissingProducts(t *testing.T) {
	svc := newProductService(t)

	views, err := svc.ListByIDs(context.Background(), []uint64{1, 404})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	if len(views) != 1 || views[1].Name != "Gofy T-Shirt" {
		t.Fatalf("unexpected views: %+v", views)
	}
	if _, ok := views[404]; ok {
		t.Fatal("missing product should be absent from the result")
	}
}

func TestDecreaseStockRespectsStatusAndStock(t *testing.T) {
	svc := newProductService(t)
	ctx := context.Background()

	if err := svc.DecreaseStock(ctx, 1, 3); err != nil {
		t.Fatalf("DecreaseStock(1, 3): %v", err)
	}
	view, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get(1): %v", err)
	}
	if view.Stock != 7 {
		t.Fatalf("stock = %d, want 7", view.Stock)
	}

	for _, tc := range []struct {
		name string
		id   uint64
		num  int
		want error
	}{
		{name: "off sale", id: 2, num: 1, want: productapi.ErrUnavailable},
		{name: "short stock", id: 1, num: 100, want: productapi.ErrInsufficientStock},
		{name: "missing product", id: 404, num: 1, want: productapi.ErrNotFound},
	} {
		err := svc.DecreaseStock(ctx, tc.id, tc.num)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: DecreaseStock error = %v, want %v", tc.name, err, tc.want)
		}
	}

	if err := svc.DecreaseStock(ctx, 1, 0); err == nil {
		t.Fatal("DecreaseStock with num 0 should fail")
	}
}
