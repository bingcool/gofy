package redis_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bingcool/gofy/support/redis"
)

func TestConfigValidate(t *testing.T) {
	if err := (redis.Config{}).Validate(); !errors.Is(err, redis.ErrAddrRequired) {
		t.Fatalf("empty addr = %v, want ErrAddrRequired", err)
	}
	cases := []redis.Config{
		{Addr: "127.0.0.1:6379", Network: "udp"},
		{Addr: "127.0.0.1:6379", DB: -1},
		{Addr: "127.0.0.1:6379", PoolSize: -1},
		{Addr: "127.0.0.1:6379", PoolSize: 2, MinIdleConns: 3},
		{Addr: "127.0.0.1:6379", ReadTimeout: -time.Second},
		{Addr: "127.0.0.1:6379", MaxRetries: -2},
	}
	for _, cfg := range cases {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate(%+v) accepted an invalid config", cfg)
		}
	}
	if err := (redis.Config{Addr: "127.0.0.1:6379"}).Validate(); err != nil {
		t.Fatalf("Validate(minimal) = %v", err)
	}
}

func TestNewPingAndClose(t *testing.T) {
	mr := miniredis.RunT(t)
	client, err := redis.New(context.Background(), redis.Config{Addr: mr.Addr()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if err := redis.Close(client); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	opts := client.Options()
	if opts.PoolSize != 10 || opts.MinIdleConns != 2 {
		t.Fatalf("pool defaults = %d/%d, want 10/2", opts.PoolSize, opts.MinIdleConns)
	}

	ctx := context.Background()
	if err := client.Set(ctx, "greeting", "hello", time.Minute).Err(); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := client.Get(ctx, "greeting").Result()
	if err != nil || got != "hello" {
		t.Fatalf("Get = (%q, %v)", got, err)
	}
	if err := redis.Ping(ctx, client, time.Second); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestNewFailsWhenServerIsUnreachable(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()

	_, err := redis.New(context.Background(), redis.Config{
		Addr:        addr,
		Password:    "secret-password",
		DialTimeout: 100 * time.Millisecond,
		MaxRetries:  -1,
		PingTimeout: time.Second,
	})
	if err == nil {
		t.Fatal("New succeeded against a closed server")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Fatalf("error hides the address: %v", err)
	}
	if strings.Contains(err.Error(), "secret-password") {
		t.Fatalf("error leaked the password: %v", err)
	}
}

func TestNilHelpers(t *testing.T) {
	if err := redis.Close(nil); err != nil {
		t.Fatalf("Close(nil) = %v", err)
	}
	if err := redis.Ping(context.Background(), nil, time.Second); err == nil {
		t.Fatal("Ping(nil) succeeded")
	}
}
