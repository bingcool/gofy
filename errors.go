package gofy

import (
	"context"
	"errors"
)

// Exit codes returned by ExitCode.
// ExitCode 可能返回的进程退出码。
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// Sentinel errors returned by the framework.
// 框架返回的哨兵错误，调用方用 errors.Is 判断，不要比较错误字符串。
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
// ExitCode 把 Execute 或 Run 返回的错误映射为进程退出码：nil 与 context.Canceled
// 视为正常退出，用法错误或未知命令返回 2，其余错误返回 1。
func ExitCode(err error) int {
	if err == nil || errors.Is(err, context.Canceled) {
		return ExitOK
	}
	if errors.Is(err, ErrUsage) || errors.Is(err, ErrUnknownCommand) {
		return ExitUsage
	}
	return ExitError
}
