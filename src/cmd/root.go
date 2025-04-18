package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/bingcool/gofy/src/cmd/command"
	_ "github.com/bingcool/gofy/src/conf"
	"github.com/bingcool/gofy/src/log"
	"github.com/bingcool/gofy/src/system"
	"github.com/bingcool/gofy/src/utils"
	"github.com/sevlyar/go-daemon"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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
	daemonCtx *daemon.Context
)

// init
func init() {
	initStartParseFlag()
}

func init() {
	rootCmd.AddCommand(StartCmd)
	rootCmd.AddCommand(StopCmd)
	rootCmd.AddCommand(DaemonCmd)
	rootCmd.AddCommand(CronCmd)
	rootCmd.AddCommand(ScriptCmd)
	rootCmd.AddCommand(VersionCmd)
}

// Execute executes the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}

// GetCommandNameMapCobraCmd 获取命令名称和 cobra.Command 的映射
func GetCommandNameMapCobraCmd() map[string]*cobra.Command {
	mapCobraCmd := map[string]*cobra.Command{
		command.StartCommandName:   StartCmd,
		command.StopCommandName:    StopCmd,
		command.DaemonCommandName:  DaemonCmd,
		command.CronCommandName:    CronCmd,
		command.ScriptCommandName:  ScriptCmd,
		command.VersionCommandName: VersionCmd,
	}
	return mapCobraCmd
}

// InitStartParseFlag 初始化命令flag参数
func initStartParseFlag() {
	command.SystemRunModel()
	args := os.Args
	if _, ok := utils.IsValidIndexOfSlice(args, 1); ok {
		if len(args) > 2 {
			commandName := args[1]
			mapCobraCmd := GetCommandNameMapCobraCmd()
			runCmd := mapCobraCmd[commandName]
			bindParseFlag(runCmd, os.Args[2:])
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

// savePidFile 保存pid文件
func savePidFile(pidFilePath string, pid int, pidFilePerm os.FileMode) {
	fullPidFilePath := GetFullPidFilePath(pidFilePath)
	dir := filepath.Dir(fullPidFilePath)

	if err := os.MkdirAll(dir, 0755); err != nil {
		panicMsg := "创建fullPidFilePath=" + fullPidFilePath + ", error=" + err.Error()
		log.SysError(panicMsg)
		panic(panicMsg)
	}

	serverPidFile, err1 := os.OpenFile(fullPidFilePath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, pidFilePerm)
	if err1 != nil {
		log.SysError("Error OpenPidFile=" + fullPidFilePath + ", error=" + err1.Error())
	}

	_, err2 := serverPidFile.WriteString(strconv.Itoa(pid))
	if err2 != nil {
		errorMsg := fmt.Sprintf("Error save server pidFile=%s, error=%s", fullPidFilePath, err2.Error())
		log.SysError(errorMsg)
		panic(errorMsg)
	}

	_ = serverPidFile.Close()
}

// GetHttpServerPid 获取服务pid
func GetHttpServerPid() int {
	pidFilePath := viper.GetString("httpServer.pidFilePath")
	fullPidFilePath := GetFullPidFilePath(pidFilePath)
	pid, err := os.ReadFile(fullPidFilePath)
	pidStr := string(pid)
	if err != nil {
		return 0
	}
	pid1, err1 := strconv.Atoi(pidStr)
	if err1 != nil {
		return 0
	}
	return pid1
}

// GetFullPidFilePath 获取pid文件绝对路径
func GetFullPidFilePath(pidFilePath string) string {
	var fullPidFilePath string
	startRootPath := system.GetStartRootPath()
	if !strings.Contains(pidFilePath, startRootPath) {
		fullPidFilePath = filepath.Join(startRootPath, pidFilePath)
	} else {
		fullPidFilePath = pidFilePath
	}
	return fullPidFilePath
}

// IsServerRunning 判断服务是否正在运行
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

// handleExitSignals 处理系统信号
func handleExitSignals() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigChan
		fmt.Println("server Shutting down gracefully...")
		log.SysInfo("server Shutting down gracefully......")
		os.Exit(0)
	}()
}
