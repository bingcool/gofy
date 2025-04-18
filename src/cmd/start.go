package cmd

import (
	"fmt"
	"os"
	"strconv"

	"github.com/bingcool/gofy/app/middleware"
	"github.com/bingcool/gofy/app/route"
	"github.com/bingcool/gofy/src/cmd/command"
	"github.com/bingcool/gofy/src/log"
	"github.com/bingcool/gofy/src/system"
	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
	"github.com/sevlyar/go-daemon"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

var StartCmd = &cobra.Command{
	Use:   command.StartCommandName,
	Short: "start the http server",
	Long:  `start the http server`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// 读取os.Args
		if len(os.Args) > 1 {
			args = os.Args[1:]
		}
		// 在每个命令执行之前执行的操作
		fmt.Println("before start run ")
		log.SysInfo("http server before start run args=")
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

// getArgs 获取命令行参数
func getArgs() []string {
	args := make([]string, 0)
	// 读取os.Args
	if len(os.Args) > 1 {
		args = os.Args[1:]
	}
	return args
}

// startRun 启动服务
func startRun(cmd *cobra.Command, args []string) {
	pidFilePath := viper.GetString("httpServer.pidFilePath")
	pidFilePerm := os.FileMode(viper.GetUint32("httpServer.pidFilePerm"))
	logFilePath := viper.GetString("scriptServer.logFilePath")
	savePidFile(pidFilePath, os.Getpid(), pidFilePerm)
	isDaemon, _ := cmd.Flags().GetInt("daemon")
	log.SysInfo("Http server read to start",
		zap.String("pidFilePath", pidFilePath),
		zap.Uint32("pidFilePerm", uint32(pidFilePerm)),
		zap.Int("pid", os.Getpid()),
		zap.Int("daemon", isDaemon),
	)
	if isDaemon > 0 {
		// 配置守护进程上下文
		daemonCtx = &daemon.Context{
			PidFileName: pidFilePath,
			PidFilePerm: pidFilePerm,
			LogFileName: logFilePath,
			LogFilePerm: 0640,
			WorkDir:     system.GetWorkRootDir(),
			Umask:       027,
		}
		// 守护进程化
		d, err := daemonCtx.Reborn()

		if err != nil {
			log.SysError(fmt.Sprintf("Error starting daemon:%s", err.Error()))
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
	// 支持使用秒级表达式（支持6位）
	cronTab := cron.New(cron.WithSeconds())
	// 添加cron任务定时记录pid
	_, _ = cronTab.AddFunc("@every 10s", func() {
		savePidFile(pidFilePath, os.Getpid(), pidFilePerm)
	})
	cronTab.Start()

	// 处理系统信号
	handleExitSignals()
	// 守护进程主逻辑
	err := startServer()
	if err == nil {
		log.SysInfo("Http server start successful!!!",
			zap.String("pidFilePath", pidFilePath),
			zap.Uint32("pidFilePerm", uint32(pidFilePerm)),
			zap.Int("pid", os.Getpid()),
			zap.Int("daemon", isDaemon),
		)
	}
}

// startServer 启动服务
func startServer() error {
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
		log.SysError(fmt.Sprintf("Error starting http server,error=%s", err.Error()),
			zap.Any("port", viper.GetInt("httpServer.port")))
	}
	return err
}
