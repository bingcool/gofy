package repository

import (
	"context"
	"testing"

	"github.com/yourorg/demo-service/app/appio"
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
