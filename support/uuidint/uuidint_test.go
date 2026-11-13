package uuidint_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	gofyredis "github.com/bingcool/gofy/support/redis"
	"github.com/bingcool/gofy/support/uuidint"
	goredis "github.com/redis/go-redis/v9"
)

// testClient returns a client connected to miniredis and closed automatically at
// the end of the test.
// testClient 返回连接 miniredis 的客户端，测试结束时自动关闭。
func testClient(t *testing.T, mr *miniredis.Miniredis) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{
		Addr:         mr.Addr(),
		MaxRetries:   -1,
		DialTimeout:  500 * time.Millisecond,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// deadClient points at an unreachable address and is used to exercise retries,
// the error callback and the fallback path.
// deadClient 指向不可达地址，用来验证重试、告警和降级路径。
func deadClient(t *testing.T) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{
		Addr:         "127.0.0.1:1",
		MaxRetries:   -1,
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestNewValidates(t *testing.T) {
	mr := miniredis.RunT(t)
	client := testClient(t, mr)

	cases := map[string]struct {
		key  string
		opts []uuidint.Option
	}{
		"empty key":            {key: " "},
		"nil client":           {key: "k", opts: []uuidint.Option{uuidint.WithClient(nil)}},
		"empty instance":       {key: "k", opts: []uuidint.Option{uuidint.WithInstance(" ")}},
		"negative ttl":         {key: "k", opts: []uuidint.Option{uuidint.WithTTL(-time.Second)}},
		"sub-second ttl":       {key: "k", opts: []uuidint.Option{uuidint.WithTTL(500 * time.Millisecond)}},
		"negative retries":     {key: "k", opts: []uuidint.Option{uuidint.WithRetry(-1, time.Millisecond)}},
		"negative retry delay": {key: "k", opts: []uuidint.Option{uuidint.WithRetry(1, -time.Millisecond)}},
		"nil fallback client":  {key: "k", opts: []uuidint.Option{uuidint.WithFallbackClients(nil)}},
		"client and instance":  {key: "k", opts: []uuidint.Option{uuidint.WithClient(client), uuidint.WithInstance("cache")}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := uuidint.New(tc.key, tc.opts...); err == nil {
				t.Fatal("New accepted an invalid configuration")
			}
		})
	}

	if _, err := uuidint.New("k", uuidint.WithClient(client)); err != nil {
		t.Fatalf("New with a client: %v", err)
	}
}

func TestGetReturnsIncreasingIDs(t *testing.T) {
	mr := miniredis.RunT(t)
	client := testClient(t, mr)
	gen, err := uuidint.New("uuid-key", uuidint.WithClient(client))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	var prev int64
	for i := 0; i < 50; i++ {
		id, err := gen.Get(ctx)
		if err != nil {
			t.Fatalf("Get #%d: %v", i, err)
		}
		if id <= prev {
			t.Fatalf("id %d is not greater than previous %d", id, prev)
		}
		prev = id
	}

	if !mr.Exists("uuid-key") {
		t.Fatal("counter key was not written")
	}
	if !mr.Exists("uuid-key_time_stamp") {
		t.Fatal("timestamp key was not written")
	}
	if ttl := mr.TTL("uuid-key"); ttl <= 0 {
		t.Fatalf("counter key ttl = %v, want > 0", ttl)
	}
}

func TestGetBatchReturnsAscendingRange(t *testing.T) {
	mr := miniredis.RunT(t)
	client := testClient(t, mr)
	gen, err := uuidint.New("uuid-key", uuidint.WithClient(client), uuidint.WithTTL(5*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	ids, err := gen.GetBatch(ctx, 5)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if len(ids) != 5 {
		t.Fatalf("len(ids) = %d, want 5", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[i-1]+1 {
			t.Fatalf("ids are not consecutive: %v", ids)
		}
	}

	next, err := gen.GetBatch(ctx, 3)
	if err != nil {
		t.Fatalf("second GetBatch: %v", err)
	}
	if len(next) != 3 {
		t.Fatalf("len(second batch) = %d, want 3", len(next))
	}
	// The in-segment counter resets across milliseconds, so only a trend of
	// increase is guaranteed, not continuity across batches.
	// 跨毫秒时段序号会重置，只保证趋势递增，不保证跨批次连续。
	if next[0] <= ids[len(ids)-1] {
		t.Fatalf("second batch = %v, want values above %d", next, ids[len(ids)-1])
	}
	for i := 1; i < len(next); i++ {
		if next[i] != next[i-1]+1 {
			t.Fatalf("second batch is not consecutive within one segment: %v", next)
		}
	}
	if ttl := mr.TTL("uuid-key"); ttl != 5*time.Second {
		t.Fatalf("ttl = %v, want 5s", ttl)
	}
}

func TestGetBatchNonPositive(t *testing.T) {
	mr := miniredis.RunT(t)
	gen, err := uuidint.New("uuid-key", uuidint.WithClient(testClient(t, mr)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ids, err := gen.GetBatch(context.Background(), 0)
	if err != nil {
		t.Fatalf("GetBatch(0): %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("GetBatch(0) = %v, want empty", ids)
	}
	if mr.Exists("uuid-key") {
		t.Fatal("GetBatch(0) must not touch Redis")
	}
}

func TestFallbackClientIsUsedWhenPrimaryFails(t *testing.T) {
	mr := miniredis.RunT(t)
	var reported error
	gen, err := uuidint.New("uuid-key",
		uuidint.WithClient(deadClient(t)),
		uuidint.WithFallbackClients(testClient(t, mr)),
		uuidint.WithRetry(1, time.Millisecond),
		uuidint.WithErrorHandler(func(err error) { reported = err }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id, err := gen.Get(context.Background())
	if err != nil {
		t.Fatalf("Get with fallback: %v", err)
	}
	if id <= 0 {
		t.Fatalf("id = %d, want > 0", id)
	}
	if reported == nil {
		t.Fatal("error handler was not called")
	}
}

func TestAllSourcesFail(t *testing.T) {
	gen, err := uuidint.New("uuid-key",
		uuidint.WithClient(deadClient(t)),
		uuidint.WithRetry(1, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = gen.Get(context.Background())
	if !errors.Is(err, uuidint.ErrUnavailable) {
		t.Fatalf("Get = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "uuid-key") && !strings.Contains(err.Error(), "connect") {
		t.Fatalf("error does not describe the failure: %v", err)
	}
}

func TestInstanceAlias(t *testing.T) {
	mr := miniredis.RunT(t)
	reg, err := gofyredis.Configure(map[string]gofyredis.Config{
		"uuid-test": {Addr: mr.Addr()},
	})
	if err != nil {
		t.Fatalf("redis.Configure: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	gen, err := uuidint.New("uuid-key", uuidint.WithInstance("uuid-test"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id, err := gen.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if id <= 0 {
		t.Fatalf("id = %d, want > 0", id)
	}
	if !mr.Exists("uuid-key") {
		t.Fatal("alias did not reach the configured instance")
	}
}

func TestUnknownInstance(t *testing.T) {
	reg, err := gofyredis.Configure(nil)
	if err != nil {
		t.Fatalf("redis.Configure: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	gen, err := uuidint.New("uuid-key", uuidint.WithInstance("missing"), uuidint.WithRetry(1, time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := gen.Get(context.Background()); !errors.Is(err, uuidint.ErrUnavailable) {
		t.Fatalf("Get = %v, want ErrUnavailable", err)
	}
}

func TestCancelledContext(t *testing.T) {
	mr := miniredis.RunT(t)
	gen, err := uuidint.New("uuid-key", uuidint.WithClient(testClient(t, mr)), uuidint.WithRetry(3, time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gen.Get(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get = %v, want context.Canceled", err)
	}
}
