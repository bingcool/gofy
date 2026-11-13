package repository

import (
	"context"
	"testing"

	appio "github.com/yourorg/demo-service/app/io"
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
		_ = r.conn(context.Background())
	}()

	db := &gorm.DB{}
	appio.PublishDB(db)
	if got := r.conn(context.Background()); got != db {
		t.Fatal("conn did not resolve the published snapshot")
	}

	pinned := &gorm.DB{}
	ctx := appio.WithSnapshot(context.Background(), appio.Current())
	appio.PublishDB(pinned)
	if got := r.conn(ctx); got != db {
		t.Fatal("conn did not keep the snapshot pinned to the request")
	}
}
