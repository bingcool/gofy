//go:build linux

package process

import (
	"os"
	"strconv"
	"strings"
)

// processToken combines the boot id with the process start time. The boot id
// alone rules out a match across reboots, the start time rules out a reused pid
// within one boot; neither is enough on its own.
// processToken 把 boot id 与进程启动时间组合起来。只有 boot id 能排除跨重启的巧合，
// 只有启动时间能在同一次开机内排除 pid 复用；单独任何一个都不够。
func processToken(pid int) string {
	start, ok := linuxStartTime(pid)
	if !ok {
		return ""
	}
	boot, ok := linuxBootID()
	if !ok {
		return ""
	}
	return boot + ":" + start
}

// linuxStartTime reads field 22 of /proc/<pid>/stat, in clock ticks since boot.
// The comm field is parenthesised and may itself contain spaces and parentheses,
// so parsing starts after the last ')': the next field is state (field 3) and
// starttime is the 20th of that remainder.
// linuxStartTime 读取 /proc/<pid>/stat 的第 22 个字段，单位是自开机以来的时钟滴答。
// comm 字段带括号且自身可能含空格与括号，因此从最后一个 ')' 之后开始解析：下一个字段是
// state（第 3 个），starttime 是剩余部分的第 20 个。
func linuxStartTime(pid int) (string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	text := string(data)
	end := strings.LastIndexByte(text, ')')
	if end < 0 || end+2 >= len(text) {
		return "", false
	}
	fields := strings.Fields(text[end+2:])
	const startTimeIndex = 19
	if len(fields) <= startTimeIndex {
		return "", false
	}
	return fields[startTimeIndex], true
}

// linuxBootID reads the identifier the kernel generates per boot. A container
// may share the host's value, which is harmless: two processes on two hosts can
// never meet through a local pid file anyway.
// linuxBootID 读取内核每次开机生成的标识。容器可能沿用宿主机的值，但这也无妨：两台主机
// 的进程本来就不会通过本地 pid 文件相遇。
func linuxBootID() (string, bool) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", false
	}
	id := strings.TrimSpace(string(data))
	return id, id != ""
}
