package pg_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/pg"
	"go.uber.org/zap"
)

func TestValidate(t *testing.T) {
	valid := pg.Config{DSN: "postgres://user:pass@localhost:5432/app"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := map[string]pg.Config{
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
	_, err := pg.New(context.Background(), pg.Config{}, zap.NewNop())
	if !errors.Is(err, pg.ErrDSNRequired) {
		t.Fatalf("error = %v, want ErrDSNRequired", err)
	}
}

func TestNewDoesNotLeakPassword(t *testing.T) {
	cases := map[string]string{
		"url":     "postgres://app:supersecret@127.0.0.1:1/app?sslmode=disable&connect_timeout=1",
		"keyword": "host=127.0.0.1 port=1 user=app password=supersecret dbname=app sslmode=disable connect_timeout=1",
	}
	for name, dsn := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := pg.New(context.Background(), pg.Config{
				DSN:         dsn,
				PingTimeout: 2 * time.Second,
			}, zap.NewNop())
			if err == nil {
				t.Skip("unexpectedly connected to 127.0.0.1:1")
			}
			if strings.Contains(err.Error(), "supersecret") {
				t.Fatalf("error leaked the password: %v", err)
			}
			if !strings.Contains(err.Error(), "***") {
				t.Fatalf("error did not redact the DSN: %v", err)
			}
		})
	}
}

func TestCloseNil(t *testing.T) {
	if err := pg.Close(nil); err != nil {
		t.Fatalf("Close(nil) = %v, want nil", err)
	}
}

// TestIntegration exercises a real PostgreSQL server when GOFY_TEST_PG_DSN is
// set, for example:
//
//	GOFY_TEST_PG_DSN='postgres://user:password@127.0.0.1:5432/app?sslmode=disable' \
//	  go test ./support/pg -run Integration -v
func TestIntegration(t *testing.T) {
	dsn := os.Getenv("GOFY_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("GOFY_TEST_PG_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := pg.New(ctx, pg.Config{
		DSN:          dsn,
		MaxOpenConns: 5,
		PingTimeout:  5 * time.Second,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() {
		if err := pg.Close(conn); err != nil {
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
