package gofy

import (
	"os"
	"strconv"

	"github.com/bingcool/gofy/internal/console"
)

func (a *App) con() *console.Console {
	return console.New(os.Stdout, os.Stderr)
}

func (a *App) printStartup(addr string) {
	c := a.con()
	c.Success("应用【%s】启动成功", a.opts.name)
	c.Table([][]string{
		{"服务应用", a.opts.name},
		{"运行状态", "running"},
		{"监听地址", "http://" + addr},
		{"运行环境", a.opts.env},
		{"进程 PID", strconv.Itoa(os.Getpid())},
		{"版本", a.version.Version},
	})
}
