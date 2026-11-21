package gofy

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsRestartIgnorableError(t *testing.T) {
	ignorable := []error{
		ErrNotRunning,
		ErrPIDFileDisabled,
		ErrUnsupported,
		fmt.Errorf("stop: %w", ErrNotRunning),
		// A wildcard stop that only found stale pid files is still "nothing to
		// stop": every leaf says so.
		// 通配符停止只遇到过期 pid 文件时仍是"没有可停的实例"：所有叶子都这么说。
		errors.Join(ErrNotRunning, fmt.Errorf("stop: %w", ErrPIDFileDisabled)),
		errors.Join(ErrNotRunning, errors.Join(ErrUnsupported, fmt.Errorf("stop: %w", ErrNotRunning))),
	}
	for _, err := range ignorable {
		if !isRestartIgnorableError(err) {
			t.Errorf("isRestartIgnorableError(%v) = false, want true", err)
		}
	}

	fatal := []error{
		ErrStopTimeout,
		ErrAlreadyRunning,
		errors.New("boom"),
		// The regression this guards: one pid file was stale, another worker
		// refused to exit. Judging the join by any single leaf would start a
		// second process on top of the one that is still running.
		// 这里守的正是回归：一个 pid 文件已过期，另一个 worker 拒绝退出。
		// 只凭任一叶子判断就会在仍在运行的进程之上再起一个。
		errors.Join(ErrNotRunning, ErrStopTimeout),
		errors.Join(fmt.Errorf("stop: %w", ErrNotRunning), fmt.Errorf("stop: %w", ErrIdentityUnverified)),
		errors.Join(ErrNotRunning, errors.New("boom")),
		errors.Join(ErrNotRunning, errors.Join(ErrPIDFileDisabled, ErrStopTimeout)),
	}
	for _, err := range fatal {
		if isRestartIgnorableError(err) {
			t.Errorf("isRestartIgnorableError(%v) = true, want false", err)
		}
	}
}
