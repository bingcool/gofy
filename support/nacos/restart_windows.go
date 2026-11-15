//go:build windows

package nacos

import "errors"

// Windows has no equivalent of exec that keeps the same pid, so the restart
// strategy is unavailable there. An exit strategy plus a supervisor covers it.
// Windows 没有能在保持同一 pid 的前提下替换进程映像的能力，因此 restart 策略不可用；
// 改用 exit 策略并交给外部监管者拉起。
var errRestartUnsupported = errors.New("nacos: restart strategy is not supported on Windows; use exit with a supervisor")

func selfRestart() error { return errRestartUnsupported }
