//go:build !windows

package process

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// NotifyEnv names the environment variable that tells a detached child which
// inherited descriptor to write its startup report to. The parent sets it, the
// child reads it; keeping the name here means the two sides cannot drift.
// NotifyEnv 是告诉脱离的子进程"向哪个继承来的描述符写启动报告"的环境变量名。父进程设置、
// 子进程读取；名字放在这里，两侧因此不会漂移。
const NotifyEnv = "GOFY_DAEMON_NOTIFY"

// NotifyFD is the descriptor the child receives the write end on. os/exec hands
// ExtraFiles to the child starting at 3, so the value is fixed by the standard
// library rather than chosen here.
// NotifyFD 是子进程收到写端的描述符。os/exec 从 3 开始把 ExtraFiles 交给子进程，因此这个
// 值由标准库决定，不是这里选的。
const NotifyFD = 3

// Detach starts executable in a new session with stdio redirected to
// /dev/null and returns its pid. The caller must not wait for the child.
// Detach 在新会话（setsid）中启动 executable，把标准输入输出重定向到 /dev/null 并返回其 pid。
// 子进程已被 Release，调用方不能再对它调用 Wait，否则会返回错误。
func Detach(executable string, args []string) (int, error) {
	pid, _, err := detach(executable, args, false)
	return pid, err
}

// DetachWithNotify is Detach plus a startup channel. The child inherits the
// write end of a pipe as descriptor 3 and learns its number from NotifyEnv, so it
// can say "ready" or name the error that stopped it. The parent reads the
// returned file and must close it; it must not print success before a line
// arrives, because the child's own stdout goes to /dev/null and a failed start
// would otherwise look exactly like a successful one.
// DetachWithNotify 在 Detach 之上加了一条启动通道。子进程以描述符 3 继承管道的写端，并从
// NotifyEnv 得知编号，因此能说出"就绪"或让它停下的错误。父进程读取返回的文件、必须关闭它，
// 且在收到一行之前不得打印成功：子进程自己的 stdout 指向 /dev/null，没有这条通道，启动失败
// 与启动成功看起来完全一样。
func DetachWithNotify(executable string, args []string) (int, *os.File, error) {
	return detach(executable, args, true)
}

func detach(executable string, args []string, notify bool) (int, *os.File, error) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, nil, fmt.Errorf("process: open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()

	if !notify {
		pid, startErr := startDetached(newDetachCommand(executable, args, devNull))
		return pid, nil, startErr
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		return 0, nil, fmt.Errorf("process: create notify pipe: %w", err)
	}
	cmd := newDetachCommand(executable, args, devNull)
	cmd.ExtraFiles = []*os.File{writer}
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d", NotifyEnv, NotifyFD))
	pid, startErr := startDetached(cmd)
	// The parent's copy of the write end has to go, otherwise the parent never
	// sees EOF when the child dies before reporting.
	// 父进程这份写端必须关掉，否则子进程在报告之前就死掉时，父进程永远看不到 EOF。
	_ = writer.Close()
	if startErr != nil {
		_ = reader.Close()
		return 0, nil, startErr
	}
	return pid, reader, nil
}

func newDetachCommand(executable string, args []string, devNull *os.File) *exec.Cmd {
	cmd := exec.Command(executable, args...)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

func startDetached(cmd *exec.Cmd) (int, error) {
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("process: start %s: %w", cmd.Path, err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return pid, fmt.Errorf("process: release %d: %w", pid, err)
	}
	return pid, nil
}
