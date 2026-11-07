package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bingcool/gofy/support/cache"
	goredis "github.com/redis/go-redis/v9"
)

func newCache(t *testing.T) (*miniredis.Miniredis, cache.Cache) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	c, err := cache.NewRedis(client)
	if err != nil {
		t.Fatalf("NewRedis: %v", err)
	}
	return mr, c
}

func TestNewRedisRequiresClient(t *testing.T) {
	if _, err := cache.NewRedis(nil); !errors.Is(err, cache.ErrClientRequired) {
		t.Fatalf("NewRedis(nil) = %v, want ErrClientRequired", err)
	}
}

func TestSetGetDeleteAndTTL(t *testing.T) {
	mr, c := newCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "order:1", "paid", time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	value, ok, err := c.Get(ctx, "order:1")
	if err != nil || !ok || value != "paid" {
		t.Fatalf("Get = %q, %v, %v", value, ok, err)
	}
	exists, err := c.Has(ctx, "order:1")
	if err != nil || !exists {
		t.Fatalf("Has = %v, %v", exists, err)
	}

	ttl, exists, err := c.TTL(ctx, "order:1")
	if err != nil || !exists || ttl != time.Minute {
		t.Fatalf("TTL = %v, %v, %v", ttl, exists, err)
	}
	mr.FastForward(30 * time.Second)
	ttl, exists, err = c.TTL(ctx, "order:1")
	if err != nil || !exists || ttl != 30*time.Second {
		t.Fatalf("TTL after fast-forward = %v, %v, %v", ttl, exists, err)
	}

	removed, err := c.Delete(ctx, "order:1")
	if err != nil || removed != 1 {
		t.Fatalf("Delete = %d, %v", removed, err)
	}
	if _, ok, err := c.Get(ctx, "order:1"); err != nil || ok {
		t.Fatalf("Get deleted = %v, %v", ok, err)
	}
	if ttl, exists, err := c.TTL(ctx, "order:1"); err != nil || exists || ttl != 0 {
		t.Fatalf("TTL deleted = %v, %v, %v", ttl, exists, err)
	}
}

func TestSetWithoutExpiry(t *testing.T) {
	_, c := newCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "forever", "value", 0); err != nil {
		t.Fatalf("Set ttl=0: %v", err)
	}
	if ttl, exists, err := c.TTL(ctx, "forever"); err != nil || !exists || ttl != 0 {
		t.Fatalf("TTL = %v, %v, %v", ttl, exists, err)
	}
	// 负数与 PHP 缓存实现一致，按“不过期”处理。
	if err := c.Set(ctx, "negative", "value", -time.Second); err != nil {
		t.Fatalf("Set negative ttl: %v", err)
	}
	if ttl, exists, err := c.TTL(ctx, "negative"); err != nil || !exists || ttl != 0 {
		t.Fatalf("TTL negative = %v, %v, %v", ttl, exists, err)
	}
}

func TestSetJSONAndGetJSON(t *testing.T) {
	_, c := newCache(t)
	ctx := context.Background()

	type order struct {
		ID     uint64 `json:"id"`
		Status string `json:"status"`
	}
	want := order{ID: 1, Status: "paid"}
	if err := c.SetJSON(ctx, "order:json", want, time.Minute); err != nil {
		t.Fatalf("SetJSON: %v", err)
	}
	var got order
	ok, err := c.GetJSON(ctx, "order:json", &got)
	if err != nil || !ok || got != want {
		t.Fatalf("GetJSON = %+v, %v, %v", got, ok, err)
	}

	// key 不存在时不修改目标值。
	got = want
	ok, err = c.GetJSON(ctx, "missing", &got)
	if err != nil || ok || got != want {
		t.Fatalf("GetJSON missing = %+v, %v, %v", got, ok, err)
	}
}

func TestGetJSONRejectsInvalidPayload(t *testing.T) {
	_, c := newCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "broken", "not-json", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	var dest map[string]string
	if _, err := c.GetJSON(ctx, "broken", &dest); err == nil {
		t.Fatal("GetJSON accepted an invalid payload")
	}
}

func TestSetManyAndGetMany(t *testing.T) {
	_, c := newCache(t)
	ctx := context.Background()

	values := map[string]string{"a": "1", "b": "2", "c": "3"}
	if err := c.SetMany(ctx, values, 0); err != nil {
		t.Fatalf("SetMany without ttl: %v", err)
	}
	got, err := c.GetMany(ctx, "a", "b", "c", "missing")
	if err != nil {
		t.Fatalf("GetMany: %v", err)
	}
	if len(got) != 3 || got["a"] != "1" || got["b"] != "2" || got["c"] != "3" {
		t.Fatalf("GetMany = %+v", got)
	}

	if err := c.SetMany(ctx, values, time.Minute); err != nil {
		t.Fatalf("SetMany with ttl: %v", err)
	}
	for _, key := range []string{"a", "b", "c"} {
		ttl, exists, err := c.TTL(ctx, key)
		if err != nil || !exists || ttl != time.Minute {
			t.Fatalf("TTL(%s) = %v, %v, %v", key, ttl, exists, err)
		}
	}

	removed, err := c.Delete(ctx, "a", "b")
	if err != nil || removed != 2 {
		t.Fatalf("Delete many = %d, %v", removed, err)
	}
}

func TestEmptyBatchesAreNoOps(t *testing.T) {
	_, c := newCache(t)
	ctx := context.Background()

	got, err := c.GetMany(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("GetMany() = %+v, %v", got, err)
	}
	if err := c.SetMany(ctx, nil, time.Minute); err != nil {
		t.Fatalf("SetMany(nil): %v", err)
	}
	if removed, err := c.Delete(ctx); err != nil || removed != 0 {
		t.Fatalf("Delete() = %d, %v", removed, err)
	}
}

func TestNilContextFallsBackToBackground(t *testing.T) {
	_, c := newCache(t)

	//nolint:staticcheck // nil context 由缓存实现兜底，便于业务在无请求上下文时调用。
	if err := c.Set(nil, "nil-ctx", "value", 0); err != nil {
		t.Fatalf("Set(nil): %v", err)
	}
	if value, ok, err := c.Get(nil, "nil-ctx"); err != nil || !ok || value != "value" {
		t.Fatalf("Get(nil) = %q, %v, %v", value, ok, err)
	}
}
