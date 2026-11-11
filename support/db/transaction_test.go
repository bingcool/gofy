package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bingcool/gofy/support/db"
	"github.com/bingcool/gofy/support/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type txItem struct {
	ID   uint64 `gorm:"primaryKey"`
	Name string
}

func (txItem) TableName() string { return "tx_items" }

func newTxDB(t *testing.T) *gorm.DB {
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
	if err := conn.AutoMigrate(&txItem{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return conn
}

func countItems(t *testing.T, conn *gorm.DB) int64 {
	t.Helper()
	var total int64
	if err := conn.Model(&txItem{}).Count(&total).Error; err != nil {
		t.Fatalf("count items: %v", err)
	}
	return total
}

func TestTransactionCommits(t *testing.T) {
	conn := newTxDB(t)
	ctx := context.Background()

	err := db.Transaction(ctx, conn, func(ctx context.Context) error {
		tx := db.WithContext(ctx, nil)
		if tx == nil {
			t.Fatal("WithContext returned nil inside a transaction")
		}
		if tx == conn {
			t.Fatal("inside a transaction WithContext should not return the base connection")
		}
		return tx.Create(&txItem{Name: "committed"}).Error
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if got := countItems(t, conn); got != 1 {
		t.Fatalf("count = %d, want 1 after commit", got)
	}
	if got := db.WithContext(ctx, conn); got != conn {
		t.Fatal("outside a transaction WithContext should return the fallback")
	}
}

func TestTransactionRollsBack(t *testing.T) {
	conn := newTxDB(t)
	boom := errors.New("boom")

	err := db.Transaction(context.Background(), conn, func(ctx context.Context) error {
		if err := db.WithContext(ctx, conn).Create(&txItem{Name: "rolled back"}).Error; err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want boom", err)
	}
	if got := countItems(t, conn); got != 0 {
		t.Fatalf("count = %d, want 0 after rollback", got)
	}
}

func TestTransactionNestedReusesOuterTx(t *testing.T) {
	conn := newTxDB(t)
	outerErr := errors.New("outer rollback")

	err := db.Transaction(context.Background(), conn, func(ctx context.Context) error {
		outer := db.WithContext(ctx, nil)
		if outer == nil {
			t.Fatal("outer transaction is missing from ctx")
		}
		if err := outer.Create(&txItem{Name: "outer"}).Error; err != nil {
			return err
		}

		err := db.Transaction(ctx, nil, func(inner context.Context) error {
			innerTx := db.WithContext(inner, conn)
			if innerTx != outer {
				t.Fatal("nested Transaction did not reuse the outer transaction")
			}
			return innerTx.Create(&txItem{Name: "inner"}).Error
		})
		if err != nil {
			return err
		}
		return outerErr
	})
	if !errors.Is(err, outerErr) {
		t.Fatalf("error = %v, want outer rollback", err)
	}
	if got := countItems(t, conn); got != 0 {
		t.Fatalf("count = %d, want 0: the nested write must roll back with the outer transaction", got)
	}
}

func TestWithContextFallback(t *testing.T) {
	conn := newTxDB(t)

	if got := db.WithContext(nil, conn); got != conn {
		t.Fatal("nil ctx should return the fallback")
	}
	if got := db.WithContext(context.Background(), conn); got != conn {
		t.Fatal("ctx without a transaction should return the fallback")
	}
	if got := db.WithContext(context.Background(), nil); got != nil {
		t.Fatal("nil fallback should stay nil")
	}
}

func TestTransactionValidatesInputs(t *testing.T) {
	conn := newTxDB(t)

	if err := db.Transaction(context.Background(), conn, nil); !errors.Is(err, db.ErrTxFuncRequired) {
		t.Fatalf("nil callback error = %v, want ErrTxFuncRequired", err)
	}
	noop := func(context.Context) error { return nil }
	if err := db.Transaction(context.Background(), nil, noop); !errors.Is(err, db.ErrTxDBRequired) {
		t.Fatalf("nil connection error = %v, want ErrTxDBRequired", err)
	}
	if err := db.Transaction(nil, conn, noop); err != nil {
		t.Fatalf("nil ctx should fall back to Background: %v", err)
	}
}
