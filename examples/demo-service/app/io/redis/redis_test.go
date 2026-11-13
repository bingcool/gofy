package redis

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	gofyredis "github.com/bingcool/gofy/support/redis"
	goredis "github.com/redis/go-redis/v9"
)

func TestOpenInstallsEnabledInstances(t *testing.T) {
	mr := miniredis.RunT(t)
	set, err := Open(context.Background(), Config{
		"cache":   {Enabled: true, Config: gofyredis.Config{Addr: mr.Addr()}},
		"session": {Enabled: true, Config: gofyredis.Config{Addr: mr.Addr(), DB: 1}},
	}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	set.Install()
	t.Cleanup(func() { _ = set.Close() })

	if Cache() == nil || Session() == nil {
		t.Fatal("enabled instances were not installed")
	}
	ctx := context.Background()
	if err := Cache().Set(ctx, "order:1", "cached", 0).Err(); err != nil {
		t.Fatalf("set cache: %v", err)
	}
	if _, err := Session().Get(ctx, "order:1").Result(); err != goredis.Nil {
		t.Fatalf("session db saw cache key: err = %v", err)
	}
}

func TestOpenInstallsRateLimit(t *testing.T) {
	mr := miniredis.RunT(t)
	set, err := Open(context.Background(), Config{
		InstanceRateLimit: {Enabled: true, Config: gofyredis.Config{Addr: mr.Addr(), DB: 3}},
	}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	set.Install()
	t.Cleanup(func() { _ = set.Close() })

	if RateLimit() == nil {
		t.Fatal("ratelimit instance was not installed")
	}
	if err := RateLimit().Ping(context.Background()).Err(); err != nil {
		t.Fatalf("ping ratelimit: %v", err)
	}
}

func TestOpenSkipsDisabledInstances(t *testing.T) {
	set, err := Open(context.Background(), Config{
		"cache":   {Enabled: false, Config: gofyredis.Config{Addr: "127.0.0.1:1"}},
		"session": {Enabled: false},
	}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	set.Install()
	t.Cleanup(func() { _ = set.Close() })

	if Cache() != nil || Session() != nil {
		t.Fatal("disabled instances were installed")
	}
}

func TestOpenRejectsUnknownInstance(t *testing.T) {
	mr := miniredis.RunT(t)
	if _, err := Open(context.Background(), Config{
		"queue": {Enabled: true, Config: gofyredis.Config{Addr: mr.Addr()}},
	}, nil); err == nil || !strings.Contains(err.Error(), "queue") {
		t.Fatalf("Open error = %v", err)
	}
}

func TestOpenClosesOpenedOnFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	_, err := Open(context.Background(), Config{
		"cache": {Enabled: true, Config: gofyredis.Config{Addr: mr.Addr()}},
		"session": {Enabled: true, Config: gofyredis.Config{
			Addr:        "127.0.0.1:1",
			DialTimeout: 100 * time.Millisecond,
			MaxRetries:  -1,
		}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("Open error = %v", err)
	}
	if Cache() != nil || Session() != nil {
		t.Fatal("failed Open installed handles")
	}
}

func TestReloadKeepsNewHandles(t *testing.T) {
	first := miniredis.RunT(t)
	second := miniredis.RunT(t)
	ctx := context.Background()

	set1, err := Open(ctx, Config{
		"cache": {Enabled: true, Config: gofyredis.Config{Addr: first.Addr()}},
	}, nil)
	if err != nil {
		t.Fatalf("Open first: %v", err)
	}
	set1.Install()
	oldClient := Cache()

	set2, err := Open(ctx, Config{
		"cache": {Enabled: true, Config: gofyredis.Config{Addr: second.Addr()}},
	}, nil)
	if err != nil {
		t.Fatalf("Open second: %v", err)
	}
	set2.Install()
	t.Cleanup(func() { _ = set2.Close() })
	if Cache() == oldClient {
		t.Fatal("Install did not replace the handle")
	}

	if err := set1.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	if Cache() == nil {
		t.Fatal("closing the old generation cleared the new handle")
	}
}

// TestIntegrationOpenRateLimit 需要真实 Redis：
//
//	GOFY_TEST_REDIS_ADDR=127.0.0.1:6379 GOFY_TEST_REDIS_PASSWORD=... \
//	  go test ./app/io/redis -run Integration -v
func TestIntegrationOpenRateLimit(t *testing.T) {
	addr := os.Getenv("GOFY_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set GOFY_TEST_REDIS_ADDR to run the Redis integration test")
	}
	ctx := context.Background()
	set, err := Open(ctx, Config{
		InstanceRateLimit: {Enabled: true, Config: gofyredis.Config{
			Addr:     addr,
			Password: os.Getenv("GOFY_TEST_REDIS_PASSWORD"),
			DB:       2,
		}},
	}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	set.Install()
	t.Cleanup(func() { _ = set.Close() })

	if RateLimit() == nil {
		t.Fatal("ratelimit handle is nil")
	}
	if err := RateLimit().Ping(ctx).Err(); err != nil {
		t.Fatalf("ping: %v", err)
	}
}
