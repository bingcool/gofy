package apprepository_test

import (
	"context"
	"errors"
	"testing"

	gofydb "github.com/bingcool/gofy/support/db"
	"github.com/bingcool/gofy/support/sqlite"
	ordermodel "github.com/yourorg/demo-service/app/module/order/model"
	orderrepo "github.com/yourorg/demo-service/app/module/order/repository"
	productmodel "github.com/yourorg/demo-service/app/module/product/model"
	productrepo "github.com/yourorg/demo-service/app/module/product/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// newCompositionDB 用纯 Go SQLite 建一个同时包含订单表和产品表的库，供跨模块
// 事务用例使用；单连接默认值保证 :memory: 只有一个库。
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
	products := productrepo.New(conn, nil)
	orders := orderrepo.New(conn, nil)

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
	products := productrepo.New(conn, nil)
	orders := orderrepo.New(conn, nil)
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
	conn := newCompositionDB(t)
	products := productrepo.New(conn, nil)
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
