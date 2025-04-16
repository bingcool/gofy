package cmd

import (
	"fmt"
	"os"

	"github.com/bingcool/gofy/src/cmd/runmodel"
	"github.com/bingcool/gofy/src/log"
	"github.com/spf13/cobra"
)

var CronCmd = &cobra.Command{
	Use:   runmodel.CronCommandName,
	Short: "start the cron server",
	Long:  `start the cron server`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// 读取os.Args
		if len(os.Args) > 1 {
			args = os.Args[1:]
		}
		// 在每个命令执行之前执行的操作
		fmt.Println("before start cron server")
		log.SysInfo("cron server before start run args=")
	},
	PreRun: func(cmd *cobra.Command, args []string) {

	},
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("start cron args=", args)
		cronRun(cmd, args)
	},
	PostRun: func(cmd *cobra.Command, args []string) {

	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {

	},
}

func cronRun(cmd *cobra.Command, args []string) {
	fmt.Println("cron server run args=", args)
}
