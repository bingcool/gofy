package script

import (
	"os"
	"strings"

	"github.com/bingcool/gofy/script/user"
	"github.com/bingcool/gofy/src/cmd/command"
	"github.com/bingcool/gofy/src/crontab"
	"github.com/spf13/cobra"
)

var scriptSchedule *map[string]func(cmd *cobra.Command)

// RegisterScriptSchedule 注册脚本
func RegisterScriptSchedule() *map[string]func(cmd *cobra.Command) {
	scriptSchedule = &map[string]func(cmd *cobra.Command){
		// 修复用户数据
		user.UserFixedCommandName: func(cmd *cobra.Command) {
			user.NewUserFixed().Handle(cmd)
		},
	}
	return scriptSchedule
}

var cronScheduleMap *map[string]*crontab.CronTaskMeta

// RegisterCronSchedule 注册定时任务
func RegisterCronSchedule() *map[string]*crontab.CronTaskMeta {
	cronScheduleMap = &map[string]*crontab.CronTaskMeta{
		// 修复用户数据
		user.UserFixedCommandName: {
			//BinFile: getBinFile(user.UserFixedCommandName),
			BinFile: "/home/wwwroot/gofy/gofy script --c=" + user.UserFixedCommandName,
			Express: "@every 10s",
			Flags: []string{
				"--type=1",
			},
			Desc: "修复用户数据",
			BetweenDateTime: []string{
				"2023-01-01 00:00:00",
				"2026-01-02 00:00:00",
			},
		},
	}
	return cronScheduleMap
}

// getBinFile 获取bin文件
func getBinFile(commandName string) string {
	elems := make([]string, 0)
	elems = append(elems, os.Args[0], command.ScriptCommandName, "--c="+commandName)
	return strings.Join(elems, " ")
}
