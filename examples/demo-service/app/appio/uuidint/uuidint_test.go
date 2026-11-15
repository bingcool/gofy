package uuidint

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	gofyuuidint "github.com/bingcool/gofy/support/uuidint"
	goredis "github.com/redis/go-redis/v9"
)

// baseConfig keeps the retry budget small so a dead primary does not cost the
// suite 3 x 150ms per case.
// baseConfig 把重试预算调小，避免每个用例为主连接故障白等 3 x 150ms。
func baseConfig() Config {
	return Config{
		Enabled:       true,
		RedisInstance: "ratelimit",
		Key:           "demo:uuid",
		RetryTimes:    1,
		RetryInterval: time.Millisecond,
	}
}

func liveClient(t *testing.T) *goredis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func deadClient(t *testing.T) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 50 * time.Millisecond,
		MaxRetries:  0,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestConfigValidateFallback covers the optional flag: absent, present with a
// distinct alias, and the same alias as the primary, which would only retry the
// connection that just failed. A disabled config ignores the whole section.
// TestConfigValidateFallback 覆盖可选开关：未配置、配置了不同别名、以及与主实例同名
// （那只是把刚失败的连接再试一遍）。禁用时整段配置都被忽略。
func TestConfigValidateFallback(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{name: "absent", mutate: func(*Config) {}},
		{name: "distinct alias", mutate: func(c *Config) { c.FallbackRedisInstance = "backup" }},
		{name: "whitespace only is absent", mutate: func(c *Config) { c.FallbackRedisInstance = "   " }},
		{name: "same as primary", mutate: func(c *Config) { c.FallbackRedisInstance = "ratelimit" }, wantErr: true},
		{name: "disabled ignores fallback", mutate: func(c *Config) {
			c.Enabled = false
			c.FallbackRedisInstance = "ratelimit"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("Validate accepted an invalid fallback")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

// TestFallbackInstanceNameTrimsWhitespace proves an empty value really means
// "no fallback", so callers can skip the client lookup entirely.
// TestFallbackInstanceNameTrimsWhitespace 确认空值确实表示"不启用备用"，调用方可以
// 完全跳过客户端查找。
func TestFallbackInstanceNameTrimsWhitespace(t *testing.T) {
	cfg := baseConfig()
	cfg.FallbackRedisInstance = "  backup  "
	if got := cfg.FallbackRedisInstanceName(); got != "backup" {
		t.Fatalf("FallbackRedisInstanceName = %q, want backup", got)
	}
	cfg.FallbackRedisInstance = "   "
	if got := cfg.FallbackRedisInstanceName(); got != "" {
		t.Fatalf("FallbackRedisInstanceName = %q, want empty", got)
	}
}

// TestOpenRequiresFallbackClient rejects a configured fallback with no client:
// degrading to the package-level alias would hide a disabled redis instance
// until the first outage.
// TestOpenRequiresFallbackClient 拒绝"配置了备用却没有客户端"：退化到包级别名会把
// 未启用的实例藏到第一次故障才暴露。
func TestOpenRequiresFallbackClient(t *testing.T) {
	cfg := baseConfig()
	cfg.FallbackRedisInstance = "backup"

	_, err := Open(cfg, nil, liveClient(t), nil)
	if err == nil || !strings.Contains(err.Error(), "backup") {
		t.Fatalf("Open error = %v, want a message naming backup", err)
	}

	set, err := Open(cfg, nil, liveClient(t), liveClient(t))
	if err != nil {
		t.Fatalf("Open with both clients: %v", err)
	}
	defer func() { _ = set.Close() }()
	if set.Generator() == nil {
		t.Fatal("generator is nil")
	}
}

// TestFallbackServesWhenPrimaryIsDown is the behaviour the option exists for:
// a broken primary still yields an ID from the backup instance.
// TestFallbackServesWhenPrimaryIsDown 正是这个选项的用途：主连接坏了仍能备用实例出号。
func TestFallbackServesWhenPrimaryIsDown(t *testing.T) {
	cfg := baseConfig()
	cfg.FallbackRedisInstance = "backup"
	set, err := Open(cfg, nil, deadClient(t), liveClient(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = set.Close() }()

	id, err := set.Generator().Get(context.Background())
	if err != nil {
		t.Fatalf("Get with fallback: %v", err)
	}
	if id <= 0 {
		t.Fatalf("id = %d, want > 0", id)
	}
}

// TestNoFallbackStillFails pins the unchanged default: without the option a
// dead primary returns ErrUnavailable rather than silently succeeding.
// TestNoFallbackStillFails 固定未配置时的原行为：主连接挂了返回 ErrUnavailable，
// 不会悄悄出号。
func TestNoFallbackStillFails(t *testing.T) {
	set, err := Open(baseConfig(), nil, deadClient(t), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = set.Close() }()

	_, err = set.Generator().Get(context.Background())
	if !errors.Is(err, gofyuuidint.ErrUnavailable) {
		t.Fatalf("Get error = %v, want ErrUnavailable", err)
	}
}

// TestDisabledIgnoresFallback keeps the disabled path a no-op even when the
// fallback section is malformed, because the generator is never built.
// TestDisabledIgnoresFallback 保证禁用时即使备用配置写错也是 no-op——生成器根本不构造。
func TestDisabledIgnoresFallback(t *testing.T) {
	cfg := baseConfig()
	cfg.Enabled = false
	cfg.FallbackRedisInstance = "ratelimit"
	set, err := Open(cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if set.Generator() != nil {
		t.Fatal("disabled config built a generator")
	}
	if err := set.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
