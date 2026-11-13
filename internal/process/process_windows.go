//go:build windows

package process

import (
	"context"
	"errors"
	"os"
	"time"
)

// ErrUnsupported is returned by process control on Windows.
// ErrUnsupported 是 Windows 上进程控制操作返回的错误，调用方可据此降级处理。
var ErrUnsupported = errors.New("process: operation not supported on windows")

// Alive reports whether pid refers to a live process.
// Alive 在 Windows 上通过发送中断信号探测 pid 是否存活，探测失败即视为不存活。
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(os.Interrupt) == nil
}

// Terminate is not supported on Windows.
// Windows 上不支持 Terminate，调用总是返回 ErrUnsupported。
func Terminate(pid int) error { return ErrUnsupported }

// Kill is not supported on Windows.
// Windows 上不支持 Kill，调用总是返回 ErrUnsupported。
func Kill(pid int) error { return ErrUnsupported }

// WaitExit is not supported on Windows.
// Windows 上不支持 WaitExit，调用总是返回 ErrUnsupported。
func WaitExit(ctx context.Context, pid int, interval time.Duration) error {
	return ErrUnsupported
}
