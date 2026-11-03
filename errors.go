package gofy

import (
	"context"
	"errors"
)

// Exit codes returned by ExitCode.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// Sentinel errors returned by the framework.
var (
	ErrAlreadyStarted  = errors.New("gofy: application already started")
	ErrAlreadyRunning  = errors.New("gofy: another instance is running")
	ErrNotRunning      = errors.New("gofy: process is not running")
	ErrStopTimeout     = errors.New("gofy: stop timeout")
	ErrPIDFileDisabled = errors.New("gofy: pid file is disabled")
	ErrUnsupported     = errors.New("gofy: operation not supported on this platform")
	ErrUnknownCommand  = errors.New("gofy: unknown command")
	ErrScriptNotFound  = errors.New("gofy: script not found")
	ErrUsage           = errors.New("gofy: usage error")
)

// ExitCode maps an error returned by Execute or Run to a process exit code.
func ExitCode(err error) int {
	if err == nil || errors.Is(err, context.Canceled) {
		return ExitOK
	}
	if errors.Is(err, ErrUsage) || errors.Is(err, ErrUnknownCommand) {
		return ExitUsage
	}
	return ExitError
}
