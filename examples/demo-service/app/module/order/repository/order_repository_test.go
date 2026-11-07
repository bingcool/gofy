package repository

import (
	"testing"

	appdb "github.com/yourorg/demo-service/app/io/db"
	"gorm.io/gorm"
)

func TestNewFromHandleResolvesInstalledHandle(t *testing.T) {
	prev := appdb.Db
	t.Cleanup(func() { appdb.Db = prev })
	appdb.Db = nil

	r := NewFromHandle(nil)
	if got := r.conn(); got != nil {
		t.Fatal("conn should be nil before app/io installs the handle")
	}

	db := &gorm.DB{}
	appdb.Db = db
	if got := r.conn(); got != db {
		t.Fatal("conn did not resolve db.Db after install")
	}
}

func TestExplicitConnectionWinsOverHandle(t *testing.T) {
	prev := appdb.Db
	t.Cleanup(func() { appdb.Db = prev })
	appdb.Db = &gorm.DB{}

	explicit := &gorm.DB{}
	if got := New(explicit, nil).conn(); got != explicit {
		t.Fatal("explicit connection should take precedence over db.Db")
	}
}
