package repository

import (
	"context"
	"testing"

	appdb "github.com/yourorg/demo-service/app/io/db"
	"gorm.io/gorm"
)

func TestNewFromHandleResolvesInstalledHandle(t *testing.T) {
	prev := appdb.Db
	t.Cleanup(func() { appdb.Db = prev })
	appdb.Db = nil

	r := NewFromHandle(nil)
	if got := r.conn(context.Background()); got != nil {
		t.Fatal("conn should be nil before app/io installs the handle")
	}

	db := &gorm.DB{}
	appdb.Db = db
	if got := r.conn(context.Background()); got != db {
		t.Fatal("conn did not resolve db.Db after install")
	}
}

func TestExplicitConnectionWinsOverHandle(t *testing.T) {
	prev := appdb.Db
	t.Cleanup(func() { appdb.Db = prev })
	appdb.Db = &gorm.DB{}

	explicit := &gorm.DB{}
	if got := New(explicit, nil).conn(context.Background()); got != explicit {
		t.Fatal("explicit connection should take precedence over db.Db")
	}
}

func TestProductViewOnSale(t *testing.T) {
	if !(ProductView{Status: StatusOnSale}).OnSale() {
		t.Fatal("StatusOnSale should report on sale")
	}
	if (ProductView{Status: 0}).OnSale() {
		t.Fatal("status 0 should not report on sale")
	}
}
