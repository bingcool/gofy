package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy"
	gofydb "github.com/bingcool/gofy/support/db"
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
	t.Cleanup(func() { current.Store(nil) })

	if Default() != defaultDB {
		t.Fatal("Default does not point at the framework connection")
	}
	if set.Instance(InstanceOrder) != nil || set.Instance(InstanceProduct) != nil {
		t.Fatal("unconfigured instances must stay nil")
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if Default() != nil {
		t.Fatal("Close must withdraw the generation it installed")
	}
}

func TestOpenRequiresDefault(t *testing.T) {
	if _, err := Open(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("Open accepted a nil default connection")
	}
}

// 非预置名字（如 selectDb）不再被拒绝：注册后由 NewIO 懒加载建连。
// Open 本身不应连接该实例，因此这里用不可达地址验证“注册但未打开”。
func TestOpenRegistersLazyInstance(t *testing.T) {
	set, err := Open(context.Background(), &gorm.DB{}, map[string]gofy.DatabaseConfig{
		"default":  {DSN: "user:pass@tcp(127.0.0.1:3306)/main"},
		"selectDb": {DSN: "user:pass@tcp(127.0.0.1:1)/select?timeout=50ms", PingTimeout: 100 * time.Millisecond},
	}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	set.Install()
	t.Cleanup(func() { _ = set.Close() })

	// 包级 NewIO 读当前一代 Set；懒加载实例首次访问才建连，应失败并带上实例名。
	_, err = NewIO(context.Background(), "selectDb")
	if err == nil {
		t.Skip("unexpectedly connected to 127.0.0.1:1")
	}
	if !strings.Contains(err.Error(), "selectDb") {
		t.Fatalf("NewIO error = %v, want the instance name", err)
	}
	if _, err := set.NewIO(context.Background(), "missing"); !errors.Is(err, gofydb.ErrUnknownInstance) {
		t.Fatalf("Set.NewIO(missing) = %v, want ErrUnknownInstance", err)
	}
	if _, err := NewIO(context.Background(), "missing"); err == nil {
		t.Fatal("NewIO accepted an unregistered instance")
	}
}

func TestOpenRejectsInvalidLazyConfig(t *testing.T) {
	_, err := Open(context.Background(), &gorm.DB{}, map[string]gofy.DatabaseConfig{
		"default":  {DSN: "user:pass@tcp(127.0.0.1:3306)/main"},
		"selectDb": {},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "selectDb") {
		t.Fatalf("Open error = %v, want the invalid instance name", err)
	}
}

// TestIntegrationOpenNamedInstances 需要真实 MySQL：
//
//	GOFY_TEST_MYSQL_DSN='user:password@tcp(127.0.0.1:3306)/app?charset=utf8mb4&parseTime=True&loc=Local' \
//	  go test ./app/appio/db -run Integration -v
func TestIntegrationOpenNamedInstances(t *testing.T) {
	dsn := os.Getenv("GOFY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set GOFY_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	ctx := context.Background()
	defaultDB, err := gofydb.New(ctx, gofy.DatabaseConfig{DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	t.Cleanup(func() {
		current.Store(nil)
		_ = gofydb.Close(defaultDB)
	})

	set, err := Open(ctx, defaultDB, map[string]gofy.DatabaseConfig{
		"default":  {DSN: dsn},
		"order":    {DSN: dsn, MaxOpenConns: 3},
		"product":  {DSN: dsn, MaxOpenConns: 3},
		"selectDb": {DSN: dsn, MaxOpenConns: 3},
	}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	set.Install()

	selectDB, err := NewIO(ctx, "selectDb")
	if err != nil {
		t.Fatalf("lazy selectDb: %v", err)
	}
	for name, pool := range map[string]*gorm.DB{
		"default":  Default(),
		"order":    set.Instance(InstanceOrder),
		"product":  set.Instance(InstanceProduct),
		"selectDb": selectDB,
	} {
		if pool == nil {
			t.Fatalf("%s handle is nil", name)
		}
		var one int
		if err := pool.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error; err != nil {
			t.Fatalf("%s SELECT 1: %v", name, err)
		}
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if Default() != nil {
		t.Fatal("extra handles were not cleared")
	}
}
