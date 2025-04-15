package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/bingcool/gofy/app/middleware"
	"github.com/bingcool/gofy/app/route"
	"github.com/gin-gonic/gin"
	"github.com/sevlyar/go-daemon"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// startCommandName is the name of the start command
var (
	daemonCtx *daemon.Context
)

var StartCmd = &cobra.Command{
	Use:   startCommandName,
	Short: "start the gofy",
	Long:  `start the gofy`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// 读取os.Args
		if len(os.Args) > 1 {
			args = os.Args[1:]
		}
		// 在每个命令执行之前执行的操作
		fmt.Println("before start run ")
	},
	PreRun: func(cmd *cobra.Command, args []string) {

	},
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("start run args=", args)
		startRun(cmd, args)
	},
	PostRun: func(cmd *cobra.Command, args []string) {

	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		// 在每个命令执行之后执行的操作
		fmt.Println("after start run")
	},
}

func getArgs() []string {
	args := make([]string, 0)
	// 读取os.Args
	if len(os.Args) > 1 {
		args = os.Args[1:]
	}
	return args
}

func startRun(cmd *cobra.Command, args []string) {
	pidFilePath := viper.GetString("httpServer.pidFilePath")
	savePidFile(pidFilePath, os.Getpid())
	isDaemon, _ := cmd.Flags().GetInt("daemon")
	if isDaemon > 0 {
		// 配置守护进程上下文
		daemonCtx = &daemon.Context{
			PidFileName: pidFilePath,
			PidFilePerm: 0644,
			LogFileName: viper.GetString("httpServer.logFilePath"),
			LogFilePerm: 0640,
			WorkDir:     "./",
			Umask:       027,
		}
		// 守护进程化
		d, err := daemonCtx.Reborn()

		if err != nil {
			fmt.Println("Error starting daemon:", err)
			os.Exit(1)
		}
		if d != nil {
			// 父进程退出
			return
		}
		defer daemonCtx.Release()
	}
	// 处理系统信号
	handleSignals()
	// 守护进程主逻辑
	startServer()
}

// handleSignals 处理系统信号
func handleSignals() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sig
		fmt.Println("Shutting down gracefully...")
		os.Exit(0)
	}()
}

// startServer 启动服务
func startServer() {
	// 将日志输出重定向到空设备（静默模式）
	engine := gin.New()
	engine.Use(gin.Recovery())
	// 设置全局中间件
	middleware.SetGlobalMiddleware(engine)
	// 注册路由
	route.RegisterRouter(engine)
	port := ":" + strconv.Itoa(viper.GetInt("httpServer.port"))
	err := engine.Run(port)
	if err != nil {
		fmt.Println("Error starting server:", err)
	}
}
