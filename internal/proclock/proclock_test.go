package proclock_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bingcool/gofy/internal/proclock"
)

// TestAcquireIsExclusive is the property the pid file could not provide: a
// second acquire on the same path fails while the first is alive. Two separate
// open file descriptions conflict even inside one process, which is what makes
// this testable without spawning a second binary.
// TestAcquireIsExclusive 是 pid 文件给不了的性质：第一个锁还活着时，同路径的第二次
// 获取必须失败。两个独立的文件描述符在同一进程里也会冲突，因此不必起第二个进程就能测。
func TestAcquireIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "group.lock")

	first, err := proclock.Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer func() { _ = first.Close() }()

	if _, err := proclock.Acquire(path); !errors.Is(err, proclock.ErrHeld) {
		t.Fatalf("second Acquire = %v, want ErrHeld", err)
	}
}

// TestAcquireAfterReleaseSucceeds proves the lock is released, not just that
// the first caller stopped looking: a restarted worker must be able to take the
// same path.
// TestAcquireAfterReleaseSucceeds 证明锁确实被释放，而不是第一个调用方停止查看：重启的
// worker 必须能拿到同一个路径。
func TestAcquireAfterReleaseSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "group.lock")

	first, err := proclock.Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := proclock.Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close second: %v", err)
	}
}

// TestAcquireRejectsEmptyPath keeps a missing configuration from silently
// becoming "no lock at all".
// TestAcquireRejectsEmptyPath 让缺失的配置不能悄悄变成"根本没有锁"。
func TestAcquireRejectsEmptyPath(t *testing.T) {
	if _, err := proclock.Acquire("  "); err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("Acquire(\"\") = %v, want a path error", err)
	}
}

// TestCloseIsIdempotent allows `defer lock.Close()` on both the acquired and the
// failed branch without a second failure turning into a confusing error.
// TestCloseIsIdempotent 让 `defer lock.Close()` 同时用在成功与失败分支上，重复关闭
// 不会变成一条令人困惑的错误。
func TestCloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "group.lock")
	lock, err := proclock.Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	var nilLock *proclock.Lock
	if err := nilLock.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file should stay behind for reuse: %v", err)
	}
}
