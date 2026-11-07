package redisstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bingcool/gofy/support/ratelimit"
	"github.com/bingcool/gofy/support/ratelimit/redisstore"
	goredis "github.com/redis/go-redis/v9"
)

func newStore(t *testing.T) (*redisstore.Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := redisstore.New(client, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, mr
}

func TestStoreSlidingWindow(t *testing.T) {
	store, mr := newStore(t)
	ctx := context.Background()

	first, err := store.Allow(ctx, "ip|GET|/orders", 2, time.Minute)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !first.Allowed || first.Limit != 2 || first.Remaining != 1 {
		t.Fatalf("first = %+v", first)
	}

	second, err := store.Allow(ctx, "ip|GET|/orders", 2, time.Minute)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !second.Allowed || second.Remaining != 0 {
		t.Fatalf("second = %+v", second)
	}

	third, err := store.Allow(ctx, "ip|GET|/orders", 2, time.Minute)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if third.Allowed {
		t.Fatalf("third = %+v, want rejection", third)
	}
	if third.RetryAfter <= 0 || third.RetryAfter > time.Minute {
		t.Fatalf("retry after = %v", third.RetryAfter)
	}

	// Advancing the fake server clock past the window frees the slots.
	mr.FastForward(61 * time.Second)
	fourth, err := store.Allow(ctx, "ip|GET|/orders", 2, time.Minute)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !fourth.Allowed {
		t.Fatalf("fourth = %+v, want allowed after the window", fourth)
	}
}

func TestStoreSeparatesKeys(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	if res, _ := store.Allow(ctx, "a", 1, time.Minute); !res.Allowed {
		t.Fatal("first key was rejected")
	}
	if res, _ := store.Allow(ctx, "a", 1, time.Minute); res.Allowed {
		t.Fatal("first key was allowed twice")
	}
	if res, _ := store.Allow(ctx, "b", 1, time.Minute); !res.Allowed {
		t.Fatal("second key shared the first bucket")
	}
}

func TestStoreInvalidArguments(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	if _, err := store.Allow(ctx, "k", 0, time.Minute); !errors.Is(err, ratelimit.ErrInvalidLimit) {
		t.Fatalf("limit error = %v", err)
	}
	if _, err := store.Allow(ctx, "k", 1, 0); !errors.Is(err, ratelimit.ErrInvalidWindow) {
		t.Fatalf("window error = %v", err)
	}
}

func TestNewRequiresClient(t *testing.T) {
	if _, err := redisstore.New(nil, ""); err == nil {
		t.Fatal("New accepted a nil client")
	}
}

func TestStoreReturnsRedisErrors(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{
		Addr:        mr.Addr(),
		MaxRetries:  -1,
		DialTimeout: 100 * time.Millisecond,
	})
	store, err := redisstore.New(client, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mr.Close()

	if _, err := store.Allow(context.Background(), "k", 1, time.Minute); err == nil {
		t.Fatal("Allow succeeded after the server closed")
	}
	_ = client.Close()
}
