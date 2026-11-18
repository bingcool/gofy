package repository

import (
	"context"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/sqlite"
	"github.com/yourorg/demo-service/app/appio"
	"github.com/yourorg/demo-service/app/module/order/model"
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

	db := &gorm.DB{}
	appio.PublishDB(db)
	if got, err := r.conn(context.Background()); err != nil || got != db {
		t.Fatalf("conn = %v, %v; want the published snapshot", got, err)
	}

	pinned := &gorm.DB{}
	ctx := appio.WithSnapshot(context.Background(), appio.Current())
	appio.PublishDB(pinned)
	if got, err := r.conn(ctx); err != nil || got != db {
		t.Fatalf("conn = %v, %v; want the snapshot pinned to the request", got, err)
	}
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
	if err := conn.AutoMigrate(&model.Order{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seeds := []model.Order{
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
