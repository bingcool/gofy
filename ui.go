package gofy

import (
	"os"
	"strconv"
	"time"

	"github.com/bingcool/gofy/internal/console"
)

func (a *App) con() *console.Console {
	return console.New(os.Stdout, os.Stderr)
}

func (a *App) printStartup(addr string) {
	c := a.con()
	c.Success("应用【%s】启动成功", a.opts.name)
	c.Table(a.startupRows(addr))
}

// startupRows is the banner as data so a test can pin its fields without
// capturing os.Stdout. The version is deliberately absent: it is already in the
// structured status document and in the "application started" log line, and
// repeating a number nobody reads at every start is noise. The start time takes
// its place — that is the one fact an operator cannot get from anywhere else
// while the process is running.
// startupRows 把启动横幅变成数据，测试无需捕获 os.Stdout 就能固定字段。这里刻意不放
// 版本：它已经在结构化状态文档与 "application started" 日志行里，每次启动重复一个没人读
// 的数字只是噪音。取而代之的是启动时间——那是进程运行期间运维无法从别处得到的唯一信息。
func (a *App) startupRows(addr string) [][]string {
	return [][]string{
		{"服务应用", a.opts.name},
		{"运行状态", "running"},
		{"启动时间", a.startedAtLabel()},
		{"监听地址", "http://" + addr},
		{"运行环境", a.opts.env},
		{"进程 PID", strconv.Itoa(os.Getpid())},
	}
}

// startedAtLabel is when the runtime was built, which is the moment this
// process began starting; a build without a runtime falls back to now.
// startedAtLabel 返回运行时构建的时刻，也就是本进程开始启动的时间；没有运行时则退回
// 当前时间。
func (a *App) startedAtLabel() string {
	started := time.Now()
	if a.rt != nil && !a.rt.StartedAt.IsZero() {
		started = a.rt.StartedAt
	}
	return started.Format("2006-01-02 15:04:05")
}
