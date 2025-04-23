package command

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bingcool/gofy/src/system"
	"github.com/bingcool/gofy/src/utils"
)

var (
	StartCommandName       = "start"
	StopCommandName        = "stop"
	DaemonStartCommandName = "daemon-start"
	DaemonStopCommandName  = "daemon-stop"
	CronStartCommandName   = "cron-start"
	CronStopCommandName    = "cron-stop"
	ScriptCommandName      = "script"
	VersionCommandName     = "version"
)

// 定义命令与运行模式的映射关系
var commandRunModel = map[string]string{
	StartCommandName:       "http",
	DaemonStartCommandName: "daemon",
	DaemonStopCommandName:  "daemon",
	CronStartCommandName:   "cron",
	CronStopCommandName:    "cron",
	ScriptCommandName:      "script",
}

var flagSyncOnce sync.Once

func init() {
	SystemRunModel()
	fmt.Println(
		fmt.Sprintf("[%s] %s: %s",
			"gofy",
			time.Now().Format("2006-01-02 15:04:05"),
			fmt.Sprintf("this runModel=%s", system.RunModel)),
	)
}

// GetCommandNameSlice 获取命令名称
func GetCommandNameSlice() []string {
	return []string{
		StartCommandName,
		StopCommandName,
		DaemonStartCommandName,
		DaemonStopCommandName,
		CronStartCommandName,
		CronStopCommandName,
		ScriptCommandName,
		VersionCommandName,
	}
}

// 根据命令获取运行模式
func getRunModel(command string) string {
	if mode, exists := commandRunModel[command]; exists {
		return mode
	}
	return "http"
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
				errorMsg := "[gofy] Args[2] Not define cmd command"
				panic(errorMsg)
			}
		}

		system.RunModel = getRunModel(args[1])
	})
}
