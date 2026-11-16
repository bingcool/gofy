//go:build windows

package process

import (
	"strconv"

	"golang.org/x/sys/windows"
)

// processToken uses the process creation time, which Windows reports as a
// FILETIME: it is fixed at creation, so a recycled pid cannot reproduce it.
// processToken 使用进程创建时间，Windows 以 FILETIME 表示：该值在创建时固定，被复用的
// pid 无法复制出同一个值。
func processToken(pid int) string {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return ""
	}
	return strconv.FormatInt(creation.Nanoseconds(), 10)
}
