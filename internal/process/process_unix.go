//go:build !windows

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// Alive reports whether pid refers to a live process.
// Alive 判断 pid 是否指向存活进程；无权限探测（EPERM）也视为存活，避免把别人的进程误判为已退出。
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

// Terminate sends SIGTERM to pid.
// Terminate 向 pid 发送 SIGTERM，属于优雅退出请求；进程是否响应由对方决定，
// 需要强制结束时再调用 Kill。
func Terminate(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("process: find %d: %w", pid, err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("process: terminate %d: %w", pid, err)
	}
	return nil
}

// Kill sends SIGKILL to pid.
// Kill 向 pid 发送 SIGKILL，进程无法捕获或清理，仅用于超时后的最后手段。
func Kill(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("process: find %d: %w", pid, err)
	}
	if err := p.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("process: kill %d: %w", pid, err)
	}
	return nil
}

// WaitExit blocks until pid exits, ctx is done, or polling fails.
// WaitExit 阻塞直到 pid 退出、ctx 结束或轮询失败；interval <= 0 时使用 100ms 轮询间隔，
// ctx 结束时返回其错误，调用方需区分超时与进程已退出。
func WaitExit(ctx context.Context, pid int, interval time.Duration) error {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if !Alive(pid) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
