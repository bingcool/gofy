package repository

import (
	"testing"

	appmysql "github.com/yourorg/demo-service/app/io/mysql"
	"gorm.io/gorm"
)

func TestNewFromHandleResolvesInstalledHandle(t *testing.T) {
	prev := appmysql.Db
	t.Cleanup(func() { appmysql.Db = prev })
	appmysql.Db = nil

	r := NewFromHandle(nil)
	if got := r.conn(); got != nil {
		t.Fatal("conn should be nil before app/io installs the handle")
	}

	db := &gorm.DB{}
	appmysql.Db = db
	if got := r.conn(); got != db {
		t.Fatal("conn did not resolve mysql.Db after install")
	}
}

func TestExplicitConnectionWinsOverHandle(t *testing.T) {
	prev := appmysql.Db
	t.Cleanup(func() { appmysql.Db = prev })
	appmysql.Db = &gorm.DB{}

	explicit := &gorm.DB{}
	if got := New(explicit, nil).conn(); got != explicit {
		t.Fatal("explicit connection should take precedence over mysql.Db")
	}
}
