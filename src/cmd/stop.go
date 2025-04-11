package cmd

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var StopCmd = &cobra.Command{
	Use:   stopCommandName,
	Short: "start the gofy",
	Long:  `start the gofy`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// 在每个命令执行之前执行的操作
		fmt.Println("before stop run ")
	},
	PreRun: func(cmd *cobra.Command, args []string) {

	},
	Run: func(cmd *cobra.Command, args []string) {
		stopRun(cmd, args)
	},
	PostRun: func(cmd *cobra.Command, args []string) {

	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		// 在每个命令执行之后执行的操作
		fmt.Println("after stop run")
	},
}

func stopRun(cmd *cobra.Command, args []string) {
	// 检查 PID 文件是否存在
	if _, err := os.Stat(pidFilePath); os.IsNotExist(err) {
		fmt.Println("Daemon is not running")
		os.Exit(1)
	}

	// 读取 PID 文件并终止进程
	pid := GetServerPid()
	if pid == 0 {
		fmt.Println("Daemon is not running")
		os.Exit(1)
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		fmt.Println("Error finding process:", err)
		os.Exit(1)
	}

	// 发送 SIGTERM 信号
	if err := process.Signal(syscall.SIGTERM); err != nil {
		fmt.Println("Error stopping daemon:", err)
		os.Exit(1)
	}

	time.Sleep(time.Second * 1)

	isRunning, _ := IsServerRunning(pid)
	if !isRunning {
		fmt.Println("进程已停止", err)
	}

}
