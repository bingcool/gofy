package repository

import (
	"context"
	"testing"

	"github.com/bingcool/gofy/support/sqlite"
	"github.com/yourorg/demo-service/app/appio"
	"go.uber.org/zap"
)

func TestConnResolvesInstalledSnapshot(t *testing.T) {
	prev := appio.Current()
	t.Cleanup(func() { appio.Publish(prev) })
	appio.Publish(nil)

	r := &ProductRepository{}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("conn did not panic without a published snapshot")
			}
		}()
		_, _ = r.conn(context.Background())
	}()

	db, err := sqlite.New(context.Background(), sqlite.Config{DSN: ":memory:", LogLevel: "silent"}, zap.NewNop())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlite.Close(db); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
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
}
