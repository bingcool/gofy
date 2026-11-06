package mysql

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/bingcool/gofy"
	gofymysql "github.com/bingcool/gofy/support/mysql"
	"gorm.io/gorm"
)

func TestOpenPublishesDefaultOnly(t *testing.T) {
	defaultDB := &gorm.DB{}
	set, err := Open(context.Background(), defaultDB, map[string]gofy.DatabaseConfig{
		"default": {DSN: "user:pass@tcp(127.0.0.1:3306)/main"},
	}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	set.Install()
	t.Cleanup(func() {
		mu.Lock()
		Db, OrderDb, ProductDb = nil, nil, nil
		mu.Unlock()
	})

	if Db != defaultDB {
		t.Fatal("Db does not point at the framework connection")
	}
	if OrderDb != nil || ProductDb != nil {
		t.Fatal("unconfigured instances must stay nil")
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if Db != defaultDB {
		t.Fatal("Close must not clear the framework connection")
	}
}

func TestOpenRequiresDefault(t *testing.T) {
	if _, err := Open(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("Open accepted a nil default connection")
	}
}

func TestOpenRejectsUnknownInstance(t *testing.T) {
	_, err := Open(context.Background(), &gorm.DB{}, map[string]gofy.DatabaseConfig{
		"default": {DSN: "user:pass@tcp(127.0.0.1:3306)/main"},
		"report":  {DSN: "user:pass@tcp(127.0.0.1:3306)/report"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("Open error = %v", err)
	}
}

// TestIntegrationOpenNamedInstances 需要真实 MySQL：
//
//	GOFY_TEST_MYSQL_DSN='user:password@tcp(127.0.0.1:3306)/app?charset=utf8mb4&parseTime=True&loc=Local' \
//	  go test ./app/io/mysql -run Integration -v
func TestIntegrationOpenNamedInstances(t *testing.T) {
	dsn := os.Getenv("GOFY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set GOFY_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	ctx := context.Background()
	defaultDB, err := gofymysql.New(ctx, gofy.DatabaseConfig{DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	t.Cleanup(func() {
		mu.Lock()
		Db, OrderDb, ProductDb = nil, nil, nil
		mu.Unlock()
		_ = gofymysql.Close(defaultDB)
	})

	set, err := Open(ctx, defaultDB, map[string]gofy.DatabaseConfig{
		"default": {DSN: dsn},
		"order":   {DSN: dsn, MaxOpenConns: 3},
		"product": {DSN: dsn, MaxOpenConns: 3},
	}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	set.Install()

	for name, db := range map[string]*gorm.DB{"default": Db, "order": OrderDb, "product": ProductDb} {
		if db == nil {
			t.Fatalf("%s handle is nil", name)
		}
		var one int
		if err := db.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error; err != nil {
			t.Fatalf("%s SELECT 1: %v", name, err)
		}
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if OrderDb != nil || ProductDb != nil {
		t.Fatal("extra handles were not cleared")
	}
}
