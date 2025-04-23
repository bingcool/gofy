package cmd

import (
	"fmt"
	"os"

	"github.com/bingcool/gofy/script"
	"github.com/bingcool/gofy/src/cmd/command"
	"github.com/bingcool/gofy/src/log"
	"github.com/bingcool/gofy/src/system"
	"github.com/sevlyar/go-daemon"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

// go run main.go script --c=fix-user --order_id=11111

var ScriptCmd = &cobra.Command{
	Use:   command.ScriptCommandName,
	Short: "run script",
	Long:  "run script",
	//Args:  cobra.MaximumNArgs(1), //使用内置的验证函数，位置参数只能一个，即命令之后的变量，这里是指脚本名称
	// 如果设置了PersistentPreRun，将会覆盖rootCmd设置的PersistentPreRun
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		fmt.Println("script PersistentPreRun")
	},
	PreRun: func(cmd *cobra.Command, args []string) {

	},
	Run: func(cmd *cobra.Command, args []string) {
		scriptRun(cmd, args)
	},
	PostRun: func(cmd *cobra.Command, args []string) {

	},
	// 如果设置了PersistentPostRun，将会覆盖rootCmd设置的PersistentPostRun
	PersistentPostRun: func(cmd *cobra.Command, args []string) {

	},
}

// scriptRun 执行脚本
func scriptRun(cmd *cobra.Command, _ []string) {
	pidFilePath := viper.GetString("scriptServer.pidFilePath")
	pidFilePerm := os.FileMode(viper.GetUint32("scriptServer.pidFilePerm"))
	logFilePath := viper.GetString("scriptServer.logFilePath")
	isDaemon, _ := cmd.Flags().GetInt("daemon")

	log.SysInfo("scriptServer script read to exec",
		zap.String("pidFilePath", pidFilePath),
		zap.String("logFilePath", logFilePath),
		zap.Uint32("pidFilePerm", uint32(pidFilePerm)),
		zap.Int("pid", os.Getpid()),
		zap.Int("daemon", isDaemon),
	)
	if isDaemon > 0 {
		// 配置守护进程上下文
		daemonCtx = &daemon.Context{
			PidFileName: "",
			PidFilePerm: pidFilePerm,
			LogFileName: "",
			LogFilePerm: 0640,
			WorkDir:     system.GetWorkRootDir(),
			Umask:       027,
		}
		// 守护进程化
		d, err := daemonCtx.Reborn()

		if err != nil {
			log.SysError(fmt.Sprintf("Error starting script server daemon process:%s", err.Error()))
			os.Exit(1)
		}
		if d != nil {
			// 父进程退出
			return
		}
		defer func(daemonCtx *daemon.Context) {
			_ = daemonCtx.Release()
		}(daemonCtx)
	}

	handleExitSignals()

	scriptScheduleList := *script.RegisterScriptSchedule()
	commandName, _ := cmd.Flags().GetString("c")
	if fn, ok := scriptScheduleList[commandName]; ok {
		fn(cmd)
	} else {
		log.SysError(fmt.Sprintf("script command [--c=%s] not found in kernel", commandName))
	}
}
