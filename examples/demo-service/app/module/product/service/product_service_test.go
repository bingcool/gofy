package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bingcool/gofy/support/sqlite"
	appio "github.com/yourorg/demo-service/app/io"
	productapi "github.com/yourorg/demo-service/app/module/product/innerapi"
	productmodel "github.com/yourorg/demo-service/app/module/product/model"
	productrepo "github.com/yourorg/demo-service/app/module/product/repository"
	"github.com/yourorg/demo-service/app/module/product/service"
	"go.uber.org/zap"
)

// newProductService 用纯 Go 内存 SQLite 建真实仓库，把连接发布成 app/io 快照，
// 不连外部 MySQL；仓库本身不持连接，因此每个用例结束后恢复快照即可。
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
	prev := appio.Current()
	appio.PublishDB(conn)
	t.Cleanup(func() { appio.Publish(prev) })
	return service.NewProductService(service.Dependencies{
		ProductRepository: &productrepo.ProductRepository{},
		Logger:            zap.NewNop(),
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

func TestListPagesNewestFirst(t *testing.T) {
	svc := newProductService(t)
	ctx := context.Background()

	views, total, err := svc.List(ctx, 1, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(views) != 1 {
		t.Fatalf("total = %d, rows = %d, want 2 and 1", total, len(views))
	}
	// 按 id 倒序：第一页第一条是 id 最大的产品，且保留下架状态。
	if views[0].ID != 2 || views[0].OnSale {
		t.Fatalf("first view = %+v", views[0])
	}

	second, total, err := svc.List(ctx, 2, 1)
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if total != 2 || len(second) != 1 || second[0].ID != 1 || !second[0].OnSale {
		t.Fatalf("page 2 = %+v, total = %d", second, total)
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
