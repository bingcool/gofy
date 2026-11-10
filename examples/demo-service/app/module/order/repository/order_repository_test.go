package repository

import (
	"context"
	"testing"

	appdb "github.com/yourorg/demo-service/app/io/db"
	"gorm.io/gorm"
)

func TestConnResolvesInstalledHandle(t *testing.T) {
	prev := appdb.Db
	t.Cleanup(func() { appdb.Db = prev })
	appdb.Db = nil

	r := &OrderRepository{}
	if got := r.conn(context.Background()); got != nil {
		t.Fatal("conn should be nil before app/io installs the handle")
	}

	db := &gorm.DB{}
	appdb.Db = db
	if got := r.conn(context.Background()); got != db {
		t.Fatal("conn did not resolve db.Db after install")
	}
}
