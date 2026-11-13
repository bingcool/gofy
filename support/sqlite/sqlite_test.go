package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/sqlite"
	"go.uber.org/zap"
)

func TestValidate(t *testing.T) {
	valid := sqlite.Config{DSN: "app.db"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := map[string]sqlite.Config{
		"missing dsn":       {},
		"negative idle":     {DSN: valid.DSN, MaxIdleConns: -1},
		"negative open":     {DSN: valid.DSN, MaxOpenConns: -1},
		"idle above open":   {DSN: valid.DSN, MaxIdleConns: 10, MaxOpenConns: 5},
		"negative lifetime": {DSN: valid.DSN, ConnMaxLifetime: -time.Second},
		"negative ping":     {DSN: valid.DSN, PingTimeout: -time.Second},
		"negative slow":     {DSN: valid.DSN, SlowThreshold: -time.Second},
		"invalid log level": {DSN: valid.DSN, LogLevel: "verbose"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate accepted an invalid config")
			}
		})
	}
}

func TestNewRejectsEmptyDSN(t *testing.T) {
	_, err := sqlite.New(context.Background(), sqlite.Config{}, zap.NewNop())
	if !errors.Is(err, sqlite.ErrDSNRequired) {
		t.Fatalf("error = %v, want ErrDSNRequired", err)
	}
}

func TestCloseNil(t *testing.T) {
	if err := sqlite.Close(nil); err != nil {
		t.Fatalf("Close(nil) = %v, want nil", err)
	}
}

func TestNewOpensFileDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "app.db")
	conn, err := sqlite.New(ctx, sqlite.Config{
		DSN:         dsn,
		PingTimeout: 5 * time.Second,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() {
		if err := sqlite.Close(conn); err != nil {
			t.Errorf("close: %v", err)
		}
	}()

	var one int
	if err := conn.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error; err != nil {
		t.Fatalf("select 1: %v", err)
	}
	if one != 1 {
		t.Fatalf("SELECT 1 = %d", one)
	}

	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	// The single-writer default of SQLite: only one connection is kept unless
	// configured otherwise.
	// SQLite 的单写默认值：不显式配置时只保留一个连接。
	if got := sqlDB.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
}

func TestNewRespectsPoolSettings(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "app.db")
	conn, err := sqlite.New(ctx, sqlite.Config{
		DSN:          dsn,
		MaxOpenConns: 2,
		MaxIdleConns: 2,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = sqlite.Close(conn) }()

	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 2 {
		t.Fatalf("MaxOpenConnections = %d, want 2", got)
	}
}

func TestNewOpensMemoryDatabase(t *testing.T) {
	ctx := context.Background()
	conn, err := sqlite.New(ctx, sqlite.Config{DSN: ":memory:"}, zap.NewNop())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = sqlite.Close(conn) }()

	if err := conn.WithContext(ctx).Exec("CREATE TABLE t (id INTEGER)").Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	var count int64
	if err := conn.WithContext(ctx).Raw("SELECT count(*) FROM t").Scan(&count).Error; err != nil {
		t.Fatalf("select: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

func TestNewReportsUnopenablePath(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "missing-dir", "app.db")
	_, err := sqlite.New(context.Background(), sqlite.Config{
		DSN:         dsn,
		PingTimeout: time.Second,
	}, zap.NewNop())
	if err == nil {
		t.Fatal("New accepted an unopenable path")
	}
	if !strings.Contains(err.Error(), "sqlite:") {
		t.Fatalf("error lacks the package prefix: %v", err)
	}
}
