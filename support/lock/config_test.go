package lock_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/lock"
)

func TestConfigDefaults(t *testing.T) {
	got := lock.Config{}.WithDefaults()
	if got.RedisInstance != lock.DefaultInstance {
		t.Fatalf("instance = %q, want %q", got.RedisInstance, lock.DefaultInstance)
	}
	if got.Expiry != lock.DefaultExpiry || got.Tries != lock.DefaultTries || got.RetryDelay != lock.DefaultRetryDelay {
		t.Fatalf("defaults = %+v", got)
	}
	// Enabled and Prefix are never invented: a missing section must not start
	// writing keys.
	if got.Enabled || got.Prefix != "" {
		t.Fatalf("defaults invented enabled/prefix: %+v", got)
	}
	if name := (lock.Config{RedisInstance: "  cache  "}).RedisInstanceName(); name != "cache" {
		t.Fatalf("RedisInstanceName = %q, want the trimmed name", name)
	}
}

func TestConfigValidate(t *testing.T) {
	if err := (lock.Config{}).Validate(); err != nil {
		t.Fatalf("a disabled config must validate: %v", err)
	}
	for _, tc := range []struct {
		name string
		cfg  lock.Config
		want string
	}{
		{name: "negative expiry", cfg: lock.Config{Enabled: true, Expiry: -time.Second}, want: "expiry must be positive"},
		{name: "zero tries", cfg: lock.Config{Enabled: true, Tries: -1}, want: "tries must be at least 1"},
		{name: "negative delay", cfg: lock.Config{Enabled: true, RetryDelay: -time.Millisecond}, want: "retry_delay must not be negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestOpenDisabledReturnsNil(t *testing.T) {
	l, err := lock.Open(lock.Config{}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if l != nil {
		t.Fatalf("Open on a disabled config returned %v, want nil", l)
	}
}

func TestOpenRejectsInvalidConfig(t *testing.T) {
	_, err := lock.Open(lock.Config{Enabled: true, Expiry: -time.Second}, nil)
	if err == nil {
		t.Fatal("Open accepted a negative expiry")
	}
	if !strings.Contains(err.Error(), "expiry must be positive") {
		t.Fatalf("Open error = %v", err)
	}
}

func TestOpenAppliesOptionsAfterTheConfig(t *testing.T) {
	var reported []error
	l, err := lock.Open(
		lock.Config{Enabled: true, RedisInstance: "cache", Prefix: "cfg", Expiry: time.Minute, Tries: 1, RetryDelay: time.Millisecond},
		nil,
		lock.WithErrorHandler(func(err error) { reported = append(reported, err) }),
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if l.Prefix() != "cfg" {
		t.Fatalf("prefix = %q, want cfg", l.Prefix())
	}
	// No client and no reachable registry: the failure has to name the instance
	// and reach the error handler, which proves the option survived the
	// config-derived defaults.
	// 没有客户端、也没有可达的注册表：失败必须指出实例名并到达错误处理，这证明该选项在
	// 由配置推导出的默认值之后仍然生效。
	_, lockErr := l.Lock(t.Context(), "k")
	if lockErr == nil {
		t.Fatal("Lock without a reachable redis succeeded")
	}
	if !strings.Contains(lockErr.Error(), `"cache"`) {
		t.Fatalf("error = %v, want the instance name", lockErr)
	}
	if len(reported) == 0 {
		t.Fatal("WithErrorHandler was not applied")
	}
}
