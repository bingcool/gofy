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
