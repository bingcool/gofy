package service_test

import (
	"context"
	"errors"
	"testing"

	gofydb "github.com/bingcool/gofy/support/db"
	"github.com/bingcool/gofy/support/sqlite"
	"github.com/yourorg/demo-service/app/appio"
	ordermodel "github.com/yourorg/demo-service/app/module/order/model"
	orderrepo "github.com/yourorg/demo-service/app/module/order/repository"
	"github.com/yourorg/demo-service/app/module/order/service"
	productmodel "github.com/yourorg/demo-service/app/module/product/model"
	productrepo "github.com/yourorg/demo-service/app/module/product/repository"
	productservice "github.com/yourorg/demo-service/app/module/product/service"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 跨模块事务用例放在 service 层：service 是组合其他模块 repository 的地方，
// 事务也由 service 开启。用纯 Go SQLite 建一个同时包含订单表和产品表的库，
// 不依赖外部 MySQL；单连接默认值保证 :memory: 只有一个库。
func newCompositionDB(t *testing.T) *gorm.DB {
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
	if err := conn.AutoMigrate(&productmodel.Product{}, &ordermodel.Order{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := conn.Create(&productmodel.Product{
		ID:     90001,
		Name:   "Gofy Demo SKU",
		Price:  1000,
		Stock:  10,
		Status: productrepo.StatusOnSale,
	}).Error; err != nil {
		t.Fatalf("seed product: %v", err)
	}
	prev := appio.Current()
	appio.PublishDB(conn)
	t.Cleanup(func() { appio.Publish(prev) })
	return conn
}

func orderCount(t *testing.T, conn *gorm.DB) int64 {
	t.Helper()
	var total int64
	if err := conn.Model(&ordermodel.Order{}).Count(&total).Error; err != nil {
		t.Fatalf("count orders: %v", err)
	}
	return total
}

func stockOf(t *testing.T, products *productrepo.ProductRepository) int {
	t.Helper()
	view, err := products.FindByID(context.Background(), 90001)
	if err != nil {
		t.Fatalf("find product: %v", err)
	}
	return view.Stock
}

func TestTransactionCommitsAcrossModules(t *testing.T) {
	conn := newCompositionDB(t)
	products := &productrepo.ProductRepository{}
	orders := &orderrepo.OrderRepository{}

	err := gofydb.Transaction(context.Background(), conn, func(ctx context.Context) error {
		affected, err := products.DecreaseStock(ctx, 90001, 3)
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("stock decrease affected no rows")
		}
		_, err = orders.Create(ctx, &ordermodel.Order{UserID: 100, Amount: 3000, Status: "pending"})
		return err
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if got := stockOf(t, products); got != 7 {
		t.Fatalf("stock = %d, want 7 after commit", got)
	}
	if got := orderCount(t, conn); got != 1 {
		t.Fatalf("orders = %d, want 1 after commit", got)
	}
}

func TestTransactionRollsBackStockAndOrderTogether(t *testing.T) {
	conn := newCompositionDB(t)
	products := &productrepo.ProductRepository{}
	orders := &orderrepo.OrderRepository{}
	boom := errors.New("order write failed")

	err := gofydb.Transaction(context.Background(), conn, func(ctx context.Context) error {
		if _, err := products.DecreaseStock(ctx, 90001, 3); err != nil {
			return err
		}
		created, err := orders.Create(ctx, &ordermodel.Order{UserID: 100, Amount: 3000, Status: "pending"})
		if err != nil {
			return err
		}
		// 同一事务内能读到刚写入的订单，说明订单仓库也消费了 ctx 中的事务。
		if _, err := orders.FindByID(ctx, created.ID); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want boom", err)
	}
	if got := stockOf(t, products); got != 10 {
		t.Fatalf("stock = %d, want 10 after rollback", got)
	}
	if got := orderCount(t, conn); got != 0 {
		t.Fatalf("orders = %d, want 0 after rollback", got)
	}
}

func TestDecreaseStockRejectsUnavailableOrInvalidRequests(t *testing.T) {
	newCompositionDB(t)
	products := &productrepo.ProductRepository{}
	ctx := context.Background()

	affected, err := products.DecreaseStock(ctx, 90001, 11)
	if err != nil {
		t.Fatalf("DecreaseStock: %v", err)
	}
	if affected != 0 {
		t.Fatalf("affected = %d, want 0 when stock is insufficient", affected)
	}
	if got := stockOf(t, products); got != 10 {
		t.Fatalf("stock = %d, want 10 after a rejected decrease", got)
	}
	if _, err := products.DecreaseStock(ctx, 90001, 0); err == nil {
		t.Fatal("non-positive num should be rejected")
	}
}

// newOrderService 在组合库上装配真实订单 service：产品 service 显式构造后以
// innerapi 接口注入，验证 service 层开启的跨模块事务。
func newOrderService(t *testing.T) *service.OrderService {
	t.Helper()
	products := productservice.NewProductService(productservice.Dependencies{
		ProductRepository: &productrepo.ProductRepository{},
		Logger:            zap.NewNop(),
	})
	return service.NewOrderService(service.Dependencies{
		OrderRepository: &orderrepo.OrderRepository{},
		ProductAPI:      products,
		Logger:          zap.NewNop(),
	})
}

func TestCreateCommitsStockAndOrderAcrossModules(t *testing.T) {
	conn := newCompositionDB(t)
	svc := newOrderService(t)

	created, err := svc.Create(context.Background(), 100, 3000, []service.CreateItem{{SkuID: 90001, Num: 3}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Status != service.StatusPending {
		t.Fatalf("status = %q, want pending", created.Status)
	}
	if got := stockOf(t, &productrepo.ProductRepository{}); got != 7 {
		t.Fatalf("stock = %d, want 7 after commit", got)
	}
	if got := orderCount(t, conn); got != 1 {
		t.Fatalf("orders = %d, want 1 after commit", got)
	}
}

func TestCreateRollsBackWhenStockInsufficient(t *testing.T) {
	conn := newCompositionDB(t)
	svc := newOrderService(t)

	// 产品上架但库存只有 10，请求 11：validateItems 通过，扣库存阶段返回
	// ErrInsufficientStock，事务回滚，不落单、库存不变。
	_, err := svc.Create(context.Background(), 100, 3000, []service.CreateItem{{SkuID: 90001, Num: 11}})
	if !errors.Is(err, service.ErrInsufficientStock) {
		t.Fatalf("Create error = %v, want ErrInsufficientStock", err)
	}
	if got := stockOf(t, &productrepo.ProductRepository{}); got != 10 {
		t.Fatalf("stock = %d, want 10 unchanged", got)
	}
	if got := orderCount(t, conn); got != 0 {
		t.Fatalf("orders = %d, want 0 after rollback", got)
	}
}
