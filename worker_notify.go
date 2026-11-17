package gofy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/internal/process"
)

// The startup channel is one line in one direction. The detached child writes
// "ready" once the group is really accepting work, or "error: ..." with the
// reason it stopped; the parent prints success only after reading it. Without
// the channel the child's stdout is /dev/null and "the process was created" is
// the only thing the parent can know.
// 启动通道是一个方向上的一行文本。脱离的子进程在分组真正能接活时写 "ready"，失败时写
// "error: ..." 说明原因；父进程读到之后才打印成功。没有这条通道，子进程的 stdout 是
// /dev/null，父进程唯一能知道的只有"进程被创建了"。

// startupNotifier is the child side of the channel. A nil notifier is valid and
// does nothing, which is what every non-daemon process gets.
// startupNotifier 是通道的子进程一侧。nil 通知器是合法的且什么都不做，所有非后台进程都是
// 这种情况。
type startupNotifier struct {
	file *os.File
	once sync.Once
}

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

// ready tells the parent the group accepts work.
// ready 告诉父进程分组已经可以接活。
func (n *startupNotifier) ready() { n.report("ready") }

// failed tells the parent why this process is about to exit. It runs before
// cleanup on purpose: the pipe is the only channel the parent reads, and cleanup
// can take as long as the shutdown timeout.
// failed 告诉父进程本进程即将退出的原因。它特意放在清理之前：管道是父进程唯一读取的通道，
// 而清理可能耗掉整个停机超时。
func (n *startupNotifier) failed(err error) {
	if err == nil {
		return
	}
	n.report("error: " + err.Error())
}

func (n *startupNotifier) report(line string) {
	if n == nil || n.file == nil {
		return
	}
	n.once.Do(func() {
		// One line, one answer: the parent reads exactly one and stops, and a
		// second write would land in a closed descriptor anyway.
		// 一行一个答案：父进程只读一行就停，第二次写入也只会落在已关闭的描述符上。
		_, _ = io.WriteString(n.file, line+"\n")
		_ = n.file.Close()
		n.file = nil
	})
}

// awaitDaemonStartup blocks until the detached child reports ready, reports a
// failure, or the budget runs out. subject names the child in errors, because
// both the HTTP instance and a worker group detach through this channel. It
// deliberately does not kill the child on timeout: a slow broker may still be
// connecting, and killing a process that is about to become healthy would turn
// a slow start into an outage.
// awaitDaemonStartup 阻塞到脱离的子进程报告就绪、报告失败，或预算耗尽。subject 是错误
// 信息里对子进程的称呼，因为 HTTP 实例与 worker 分组共用这条通道。超时时它刻意不杀子
// 进程：慢的 broker 可能仍在建连，杀掉一个即将健康的进程会把"启动慢"变成一次故障。
func awaitDaemonStartup(notify *os.File, pid int, budget time.Duration, subject string) error {
	if budget <= 0 {
		budget = commandline.DefaultStartupTimeout
	}
	if err := notify.SetReadDeadline(time.Now().Add(budget)); err != nil {
		return fmt.Errorf("gofy: daemon startup channel: %w", err)
	}
	line, err := bufio.NewReader(notify).ReadString('\n')
	if err != nil {
		switch {
		case errors.Is(err, os.ErrDeadlineExceeded):
			return fmt.Errorf("gofy: %s (pid %d) has not reported ready within %s; it is still running, check its logs", subject, pid, budget)
		case errors.Is(err, io.EOF):
			return fmt.Errorf("gofy: %s (pid %d) exited before reporting ready; its own output holds the reason", subject, pid)
		default:
			return fmt.Errorf("gofy: daemon startup report: %w", err)
		}
	}
	status := strings.TrimSpace(line)
	if status == "ready" {
		return nil
	}
	if reason, ok := strings.CutPrefix(status, "error: "); ok {
		return fmt.Errorf("gofy: %s failed to start: %s", subject, reason)
	}
	return fmt.Errorf("gofy: unexpected daemon startup report %q", status)
}
