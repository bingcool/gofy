package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	middleware "github.com/bingcool/gofy/app/middleware"
	"github.com/bingcool/gofy/app/route"
	"github.com/bingcool/gofy/src/log"
	"github.com/gin-gonic/gin"
	"github.com/sevlyar/go-daemon"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

// startCommandName is the name of the start command
var (
	daemonCtx   *daemon.Context
	pidFilePath = "mydaemon.pid" // PID 文件路径
	logFilePath = "/dev/stdout"  // 日志文件路径
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
	// 配置守护进程上下文
	daemonCtx = &daemon.Context{
		PidFileName: pidFilePath,
		PidFilePerm: 0644,
		LogFileName: logFilePath,
		LogFilePerm: 0640,
		WorkDir:     "./",
		Umask:       027,
	}
	isDaemon, _ := cmd.Flags().GetInt("daemon")
	if isDaemon > 0 {
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

	log.Info("hello", zap.String("name", "bingcool"))

	log.SysError("ggggggggggggg")
	// 处理系统信号
	handleSignals()

	fmt.Println("Starting daemon=" + fmt.Sprintf("%d", isDaemon))
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
	router := gin.Default()
	// 设置全局中间件
	middleware.SetGlobalMiddleware(router)
	// 注册路由
	route.RegisterRouter(router)
	port := ":" + strconv.Itoa(viper.GetInt("httpServer.port"))
	err := router.Run(port)
	if err != nil {
		fmt.Println("Error starting server:", err)
	}
}
