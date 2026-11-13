package repository

import (
	"context"
	"testing"

	appio "github.com/yourorg/demo-service/app/io"
	"gorm.io/gorm"
)

func TestConnResolvesInstalledSnapshot(t *testing.T) {
	t.Cleanup(func() { appio.Publish(nil) })
	appio.Publish(nil)

	r := &OrderRepository{}
	if got := r.conn(context.Background()); got != nil {
		t.Fatal("conn should be nil before app/io publishes a snapshot")
	}

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
