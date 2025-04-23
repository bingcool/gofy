package script

import (
	"github.com/bingcool/gofy/script/user"
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
