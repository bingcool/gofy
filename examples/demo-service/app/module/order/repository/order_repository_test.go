package repository

import (
	"context"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/sqlite"
	"github.com/yourorg/demo-service/app/appio"
	"github.com/yourorg/demo-service/app/module/order/model/ordermodel"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestConnResolvesInstalledSnapshot(t *testing.T) {
	prev := appio.Current()
	t.Cleanup(func() { appio.Publish(prev) })
	appio.Publish(nil)

	r := &OrderRepository{}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("conn did not panic without a published snapshot")
			}
		}()
		_, _ = r.conn(context.Background())
	}()

	db := openTestDB(t)
	appio.PublishDB(db)
	got, err := r.conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	// DBConnect 会把本次 ctx 绑到句柄上（返回同一条连接上的新句柄），所以比较
	// ConnPool 而不是指针：指针不同不代表换了连接。
	if got.Statement.ConnPool != db.Statement.ConnPool {
		t.Fatal("conn did not use the published snapshot")
	}

	pinned := openTestDB(t)
	ctx := appio.WithSnapshot(context.Background(), appio.Current())
	appio.PublishDB(pinned)
	got, err = r.conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	if got.Statement.ConnPool != db.Statement.ConnPool {
		t.Fatal("conn did not keep the snapshot pinned to the request")
	}
}

// openTestDB 返回一个独立的纯 Go SQLite 连接，仅用于需要真实 *gorm.DB 的断言。
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := sqlite.New(context.Background(), sqlite.Config{DSN: ":memory:", LogLevel: "silent"}, zap.NewNop())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlite.Close(db); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	return db
}

func TestListAppliesStatusAndTimeWindow(t *testing.T) {
	conn, err := sqlite.New(context.Background(), sqlite.Config{DSN: ":memory:", LogLevel: "silent"}, zap.NewNop())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlite.Close(conn); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	if err := conn.AutoMigrate(&ordermodel.Order{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seeds := []ordermodel.Order{
		{ID: 1, UserID: 102, Amount: 1000, Status: "paid", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 2, UserID: 102, Amount: 2000, Status: "pending", CreatedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 3, UserID: 100, Amount: 3000, Status: "paid", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	if err := conn.Create(&seeds).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	prev := appio.Current()
	appio.PublishDB(conn)
	t.Cleanup(func() { appio.Publish(prev) })

	from := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		filter ListFilter
		want   []uint64
	}{
		{"user only", ListFilter{UserID: 102}, []uint64{2, 1}},
		{"status", ListFilter{UserID: 102, Status: "pending"}, []uint64{2}},
		{"from", ListFilter{UserID: 102, CreatedFrom: &from}, []uint64{2}},
		{"to", ListFilter{UserID: 102, CreatedTo: &to}, []uint64{1}},
		{"window", ListFilter{UserID: 102, CreatedFrom: &from, CreatedTo: &to}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, total, err := (&OrderRepository{}).List(context.Background(), tc.filter, 0, 10)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if total != int64(len(tc.want)) || len(rows) != len(tc.want) {
				t.Fatalf("rows = %+v, total = %d; want ids %v", rows, total, tc.want)
			}
			for i, id := range tc.want {
				if rows[i].ID != id {
					t.Fatalf("row %d id = %d, want %d", i, rows[i].ID, id)
				}
			}
		})
	}
}
