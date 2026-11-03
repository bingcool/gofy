//go:build windows

package process

import (
	"context"
	"errors"
	"os"
	"time"
)

// ErrUnsupported is returned by process control on Windows.
var ErrUnsupported = errors.New("process: operation not supported on windows")

// Alive reports whether pid refers to a live process.
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
func Terminate(pid int) error { return ErrUnsupported }

// Kill is not supported on Windows.
func Kill(pid int) error { return ErrUnsupported }

// WaitExit is not supported on Windows.
func WaitExit(ctx context.Context, pid int, interval time.Duration) error {
	return ErrUnsupported
}
