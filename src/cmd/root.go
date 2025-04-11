package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	_ "github.com/bingcool/gofy/src/conf"
	_ "github.com/bingcool/gofy/src/system"
	"github.com/bingcool/gofy/src/utils"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "gofy",
	Short: "My Gin application",
	// 如果rootCmd定义了PersistentPreRun、PersistentPostRun 两个函数对子命令同样生效,但一旦子命令重写了该函数，则父级的将不会再生效
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
	},
	Run: func(cmd *cobra.Command, args []string) {

	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
	},
}

var (
	startCommandName = "start"
	stopCommandName  = "stop"
)

// init
func init() {
	initStartParseFlag()
}

func init() {
	rootCmd.AddCommand(StartCmd)
	rootCmd.AddCommand(StopCmd)
	//rootCmd.AddCommand(VersionCmd)
	//rootCmd.AddCommand(ScriptCmd)
	//rootCmd.AddCommand(DaemonStartCmd)
	//rootCmd.AddCommand(DaemonStartAllCmd)
	//rootCmd.AddCommand(DaemonStopCmd)
	//rootCmd.AddCommand(DaemonStopAllCmd)
	//rootCmd.AddCommand(CronStartCmd)
	//rootCmd.AddCommand(CronStopCmd)
}

// Execute executes the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// initStartParseFlag 初始化命令flag参数
func initStartParseFlag() {
	args := os.Args
	if len(args) == 1 {
		args = append(args, "start")
		os.Args = args
	} else if len(args) >= 2 {
		if !utils.InSlice(args[1], []string{
			startCommandName,
		}) {
			fmt.Errorf("os.Args[2] error")
		}
	}

	if _, ok := utils.IsValidIndexOfSlice(args, 1); ok {
		if len(args) > 2 {
			bindParseFlag(StartCmd, os.Args[2:])
		}
	}
}

// bindParseFlag bind console flag params name
func bindParseFlag(runCmd *cobra.Command, args []string) {
	charsToRemove := "-"
	for _, v := range args {
		item := strings.Split(v, "=")
		if len(item) != 2 {
			continue
		}

		flagName := strings.Replace(item[0], charsToRemove, "", 2)
		flagValue := item[1]
		// flag已存在则跳过
		if runCmd.Flags().Lookup(flagName) != nil {
			continue
		}

		var flagNameString string
		var flagNameInt int
		var flagNameInt64 int64
		var flagNameFloat float64

		if utils.IsNumber(flagValue) {
			if utils.IsInt(flagValue) {
				flagValueNumber, _ := strconv.Atoi(flagValue)
				if utils.IsInt(flagValue) {
					runCmd.Flags().IntVar(&flagNameInt, flagName, flagValueNumber, "int flags params")
				} else {
					runCmd.Flags().Int64Var(&flagNameInt64, flagName, int64(flagValueNumber), "int flags params")
				}
			} else if utils.IsFloat(flagValue) {
				flagValueFloat, _ := strconv.ParseFloat(flagValue, 64)
				runCmd.Flags().Float64Var(&flagNameFloat, flagName, flagValueFloat, "float flags params")
			}
		} else {
			runCmd.Flags().StringVar(&flagNameString, flagName, flagValue, "string flags params")
		}
	}
}

func GetServerPid() int {
	pid1, err := os.ReadFile(pidFilePath)
	pidStr := string(pid1)
	if err != nil {
		return 0
	}
	pid, err1 := strconv.Atoi(pidStr)
	if err1 != nil {
		return 0
	}
	return pid
}

func IsServerRunning(pid int) (bool, error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}

	// 发送信号 0（仅检测进程是否存在）
	err = process.Signal(syscall.Signal(0))
	if err != nil {
		return false, nil
	}
	return true, nil
}
