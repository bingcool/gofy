//go:build !windows

package gofy

import (
	"os"
	"strconv"
	"strings"

	"github.com/bingcool/gofy/internal/process"
)

// notifierFromEnv builds the child's notifier from the environment the parent
// set. It validates the descriptor instead of trusting the number: an fd that is
// not open makes os.NewFile return nil, and a process started by hand with a
// stray variable set must not write into an unrelated descriptor.
// notifierFromEnv 根据父进程设置的环境变量构造子进程的通知器。它校验描述符而不是盲信编号：
// 没打开的 fd 会让 os.NewFile 返回 nil，而手工启动、环境里恰好有同名变量的进程也不该往无关
// 描述符里写东西。
func notifierFromEnv() *startupNotifier {
	raw := strings.TrimSpace(os.Getenv(process.NotifyEnv))
	if raw == "" {
		return nil
	}
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < process.NotifyFD {
		return nil
	}
	file := os.NewFile(uintptr(fd), "gofy-daemon-notify")
	if file == nil {
		return nil
	}
	return &startupNotifier{file: file}
}
