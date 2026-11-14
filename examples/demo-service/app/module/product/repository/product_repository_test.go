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

	r := &ProductRepository{}
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
}
