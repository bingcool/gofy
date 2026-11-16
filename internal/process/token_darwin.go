//go:build darwin

package process

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// processToken combines the boot time with the process start time, the same pair
// Linux gets from the boot id and /proc: one part rules out a reused pid, the
// other rules out a coincidence across reboots.
// processToken 把开机时间与进程启动时间组合起来，与 Linux 从 boot id 和 /proc 得到的
// 是同一对信息：一部分排除 pid 复用，另一部分排除跨重启的巧合。
func processToken(pid int) string {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil {
		return ""
	}
	boot, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return ""
	}
	start := info.Proc.P_starttime
	if start.Sec == 0 && start.Usec == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%06d:%d.%06d", boot.Sec, boot.Usec, start.Sec, start.Usec)
}
