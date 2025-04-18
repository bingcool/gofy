package command

import (
	"os"
	"sync"

	"github.com/bingcool/gofy/src/system"
	"github.com/bingcool/gofy/src/utils"
)

var (
	StartCommandName   = "start"
	StopCommandName    = "stop"
	DaemonCommandName  = "daemon"
	CronCommandName    = "cron"
	ScriptCommandName  = "script"
	VersionCommandName = "version"
)

var flagSyncOnce sync.Once

func init() {
	SystemRunModel()
}

// SystemRunModel 初始化系统运行模式
func SystemRunModel() {
	flagSyncOnce.Do(func() {
		args := os.Args
		if len(args) == 1 {
			args = append(args, "start")
			os.Args = args
		} else if len(args) >= 2 {
			if !utils.InSlice(args[1], GetCommandNameSlice()) {
				errorMsg := "Args[2] Not define cmd command"
				panic(errorMsg)
			}
		}

		switch args[1] {
		case StartCommandName:
			system.RunModel = "http"
		case DaemonCommandName:
			system.RunModel = "daemon"
		case CronCommandName:
			system.RunModel = "cron"
		case ScriptCommandName:
			system.RunModel = "script"
		default:
			system.RunModel = "http"
		}
	})
}

// GetCommandNameSlice 获取命令名称
func GetCommandNameSlice() []string {
	return []string{
		StartCommandName,
		StopCommandName,
		DaemonCommandName,
		CronCommandName,
		ScriptCommandName,
		VersionCommandName,
	}
}
