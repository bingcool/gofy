//go:build !windows

package nacos

import (
	"fmt"
	"os"
	"syscall"
)

// selfRestart replaces the current process image with a fresh execution of the
// same binary and arguments. The pid, process group and stdio survive, while Go
// state, deferred calls and close-on-exec descriptors do not, so the new
// process enters main from the beginning and pulls the config again. Port
// bindings are released because Go marks listener descriptors close-on-exec.
// selfRestart 用同一二进制与参数重新执行，替换当前进程映像。pid、进程组与 stdio
// 保留；Go 内存状态、defer 以及 close-on-exec 的 fd 不会保留，因此新进程从 main
// 开头重新运行并再次拉取配置。Go 给监听 fd 设了 close-on-exec，端口会随之释放。
func selfRestart() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("nacos: resolve executable: %w", err)
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		return fmt.Errorf("nacos: re-exec %s: %w", exe, err)
	}
	return nil
}
