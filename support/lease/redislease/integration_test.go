package redislease_test

// Real-Redis integration scenarios: the miniredis suite next door proves the
// Lua semantics; these prove the network behaviour — renewal against a live
// TTL, expiry handover, and recovery after the server stops answering.
// Gated on GOFY_TEST_REDIS_ADDR; without it everything here skips.
// 旁边的 miniredis 套件证明 Lua 语义；这里证明网络行为——对真实 TTL 的续期、
// 过期交接、以及服务端短暂失联后的恢复。由 GOFY_TEST_REDIS_ADDR 显式开启。

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bingcool/gofy/support/lease"
	"github.com/bingcool/gofy/support/lease/redislease"
)

const integrationTTL = 500 * time.Millisecond

func integrationAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("GOFY_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("GOFY_TEST_REDIS_ADDR is not set")
	}
	return addr
}

func integrationLocker(t *testing.T) (*redislease.Locker, *goredis.Client) {
	t.Helper()
	addr := integrationAddr(t)
	client := goredis.NewClient(&goredis.Options{
		Addr: addr,
		// Real-network retries: the pause test relies on the driver retrying
		// while the server is blocked, exactly like a production blip.
		// 真实网络重试：pause 用例依赖驱动在服务端阻塞期间重试，与生产抖动一致。
		MaxRetries:      5,
		MinRetryBackoff: 50 * time.Millisecond,
		MaxRetryBackoff: 200 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	l, err := redislease.New(client, redislease.Config{Prefix: "gofy-itx"})
	if err != nil {
		t.Fatalf("redislease.New: %v", err)
	}
	return l, client
}

// waitFor Lease expiry without flaking: poll instead of a single sleep.
// 用轮询而不是单次 sleep 等待租约过期，避免抖动。
func waitHeld(t *testing.T, l *redislease.Locker, key string) lease.Lease {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		held, err := l.Acquire(context.Background(), key, integrationTTL)
		if err == nil {
			return held
		}
		if !errors.Is(err, lease.ErrHeld) {
			t.Fatalf("Acquire %q: %v", key, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease %q was never released", key)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestIntegrationRenewKeepsLeaseAlive(t *testing.T) {
	l, _ := integrationLocker(t)
	ctx := context.Background()

	held, err := l.Acquire(ctx, "renew-keeps-alive", integrationTTL)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Keep renewing for well over one TTL. A second locker must never win
	// while renewal runs, because every renew pushes the expiry forward.
	// 在远超一个 TTL 的时间内持续续期。续期期间第二个持有者永远抢不到，
	// 因为每次续期都把过期时间往后推。
	deadline := time.Now().Add(3 * integrationTTL)
	for time.Now().Before(deadline) {
		if err := held.Renew(ctx); err != nil {
			t.Fatalf("Renew while holding: %v", err)
		}
		if _, err := l.Acquire(ctx, "renew-keeps-alive", integrationTTL); !errors.Is(err, lease.ErrHeld) {
			t.Fatalf("rival Acquire during renewal = %v, want ErrHeld", err)
		}
		time.Sleep(integrationTTL / 4)
	}

	if err := held.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestIntegrationExpiredLeaseIsTakenOver(t *testing.T) {
	l, _ := integrationLocker(t)
	ctx := context.Background()

	stale, err := l.Acquire(ctx, "expired-takeover", integrationTTL)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Let the TTL lapse; the new holder's token must be strictly larger, and
	// the stale holder loses both renew and release rights.
	// 等 TTL 过去；新持有者的 token 必须严格更大，旧持有者的续期与释放都失效。
	fresh := waitHeld(t, l, "expired-takeover")
	if fresh.Token() <= stale.Token() {
		t.Fatalf("new token %d is not larger than stale token %d", fresh.Token(), stale.Token())
	}
	if err := stale.Renew(ctx); !errors.Is(err, lease.ErrLeaseLost) {
		t.Fatalf("stale Renew = %v, want ErrLeaseLost", err)
	}
	// Release on a lost lease is deliberately silent.
	// 对已丢失租约的 Release 刻意保持静默。
	if err := stale.Release(ctx); err != nil {
		t.Fatalf("stale Release must stay silent, got %v", err)
	}

	if err := fresh.Release(ctx); err != nil {
		t.Fatalf("fresh Release: %v", err)
	}
}

func TestIntegrationOperationsRecoverAfterClientPause(t *testing.T) {
	l, client := integrationLocker(t)
	ctx := context.Background()

	held, err := l.Acquire(ctx, "pause-recovery", integrationTTL)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// CLIENT PAUSE blocks every command for a window — the closest single-node
	// analogue of a network blip or a primary failover pause. The lease TTL is
	// much longer than the pause, so the lease survives; the driver retries
	// transparently and the next operation succeeds.
	// CLIENT PAUSE 让所有命令阻塞一小段时间——这是单机节点上最接近网络抖动或
	// 主从切换暂停的手段。租约 TTL 远长于 pause，租约得以存活；驱动透明重试，
	// 下一个操作照常成功。
	if err := client.Do(ctx, "CLIENT", "PAUSE", "300ms").Err(); err != nil {
		t.Skipf("CLIENT PAUSE unavailable on this server: %v", err)
	}

	pauseDeadline := time.Now().Add(5 * time.Second)
	for {
		err := held.Renew(ctx)
		if err == nil {
			break
		}
		if !errors.Is(err, lease.ErrLeaseLost) && time.Now().After(pauseDeadline) {
			t.Fatalf("operations never recovered after CLIENT PAUSE: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// After recovery the lease is still ours: a rival cannot take it.
	// 恢复后租约仍属于我们：竞争者抢不走。
	if _, err := l.Acquire(ctx, "pause-recovery", integrationTTL); !errors.Is(err, lease.ErrHeld) {
		t.Fatalf("rival Acquire after recovery = %v, want ErrHeld", err)
	}
}
