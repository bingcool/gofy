package cmd

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/bingcool/gofy/src/cmd/command"
	"github.com/bingcool/gofy/src/log"
	"github.com/bingcool/gofy/src/system"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

var StopCmd = &cobra.Command{
	Use:   command.StopCommandName,
	Short: "stop the gofy",
	Long:  "stop the gofy",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// 在每个命令执行之前执行的操作
		fmt.Println("before stop run ")
	},
	PreRun: func(cmd *cobra.Command, args []string) {

	},
	Run: func(cmd *cobra.Command, args []string) {
		stopServer()
	},
	PostRun: func(cmd *cobra.Command, args []string) {

	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		// 在每个命令执行之后执行的操作
		fmt.Println("after stop run")
	},
}

// stopRun停止服务
func stopServer() {
	// 检查 PID 文件是否存在
	var pidFilePath string
	if system.IsCliService() {
		pidFilePath = viper.GetString("httpServer.pidFilePath")
	} else if system.IsDaemonService() {
		pidFilePath = viper.GetString("daemonServer.pidFilePath")
	} else if system.IsCronService() {
		pidFilePath = viper.GetString("cronServer.pidFilePath")
	} else if system.IsScriptService() {
		pidFilePath = viper.GetString("scriptServer.pidFilePath")
	} else {
		pidFilePath = viper.GetString("httpServer.pidFilePath")
	}

	if _, err := os.Stat(pidFilePath); os.IsNotExist(err) {
		log.SysInfo("Server pid file is not exist", zap.String("pidFilePath", pidFilePath))
		os.Exit(1)
	}

	// 读取 PID 文件并终止进程
	pid := GetServerPid(pidFilePath)
	log.SysInfo("Server ready to stop!!!", zap.String("pidFilePath", pidFilePath), zap.Int("pid", pid))

	if pid == 0 {
		errorMsg := fmt.Sprintf("Server is not running")
		log.FmtPrint(errorMsg)
		log.SysInfo(errorMsg, zap.String("pidFilePath", pidFilePath), zap.Int("pid", pid))
		os.Exit(1)
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		errorMsg := fmt.Sprintf("Server is not find process by pid")
		log.FmtPrint(errorMsg)
		log.SysError(errorMsg, zap.Int("pid", pid))
		os.Exit(1)
	}

	// send SIGTERM Signal
	if err1 := process.Signal(syscall.SIGTERM); err1 != nil {
		log.SysInfo("Server send SIGTERM Signal", zap.String("pidFilePath", pidFilePath), zap.Int("pid", pid))
		os.Exit(0)
	}

	time.Sleep(time.Second * 1)

	isRunning, _ := IsServerRunning(pid)
	if !isRunning {
		log.SysInfo("Server had Stop Stop Stop", zap.String("pidFilePath", pidFilePath), zap.Int("pid", pid))
		log.FmtPrint("Server had Stop Stop Stop!!!")
	}
}
