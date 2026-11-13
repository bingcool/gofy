package db_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/db"
	"go.uber.org/zap"
)

func TestValidate(t *testing.T) {
	valid := db.Config{DSN: "user:pass@tcp(localhost:3306)/app"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := map[string]db.Config{
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
	_, err := db.New(context.Background(), db.Config{}, zap.NewNop())
	if !errors.Is(err, db.ErrDSNRequired) {
		t.Fatalf("error = %v, want ErrDSNRequired", err)
	}
}

func TestNewDoesNotLeakPassword(t *testing.T) {
	cfg := db.Config{
		DSN:         "swoolefy:supersecret@tcp(127.0.0.1:1)/swoolefy?timeout=100ms",
		PingTimeout: 200 * time.Millisecond,
	}
	_, err := db.New(context.Background(), cfg, zap.NewNop())
	if err == nil {
		t.Skip("unexpectedly connected to 127.0.0.1:1")
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("error leaked the password: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("error did not redact the DSN: %v", err)
	}
}

func TestCloseNil(t *testing.T) {
	if err := db.Close(nil); err != nil {
		t.Fatalf("Close(nil) = %v, want nil", err)
	}
}

// TestIntegration exercises a real MySQL server when GOFY_TEST_MYSQL_DSN is
// set, for example:
// 设置 GOFY_TEST_MYSQL_DSN 后测试连接真实 MySQL，未设置时自动 skip；示例如下：
//
//	GOFY_TEST_MYSQL_DSN='user:password@tcp(127.0.0.1:3306)/app?charset=utf8mb4&parseTime=True&loc=Local' \
//	  go test ./support/db -run Integration -v
func TestIntegration(t *testing.T) {
	dsn := os.Getenv("GOFY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("GOFY_TEST_MYSQL_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := db.New(ctx, db.Config{
		DSN:          dsn,
		MaxOpenConns: 5,
		PingTimeout:  5 * time.Second,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() {
		if err := db.Close(conn); err != nil {
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
	stats := sqlDB.Stats()
	if stats.MaxOpenConnections != 5 {
		t.Fatalf("MaxOpenConnections = %d, want 5", stats.MaxOpenConnections)
	}
}
