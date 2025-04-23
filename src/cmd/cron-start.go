package cmd

import (
	"fmt"
	"os"

	"github.com/bingcool/gofy/src/cmd/command"
	"github.com/bingcool/gofy/src/crontab"
	"github.com/bingcool/gofy/src/log"
	"github.com/bingcool/gofy/src/system"
	"github.com/robfig/cron/v3"
	"github.com/sevlyar/go-daemon"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

var CronStartCmd = &cobra.Command{
	Use:   command.CronStartCommandName,
	Short: "start the cron server",
	Long:  "start the cron server",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if len(os.Args) > 1 {
			args = os.Args[1:]
		}

		fmt.Println("cron cron")
	},
	PreRun: func(cmd *cobra.Command, args []string) {

	},
	Run: func(cmd *cobra.Command, args []string) {
		cronRun(cmd, args)
	},
	PostRun: func(cmd *cobra.Command, args []string) {

	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {

	},
}

// cronRun 启动cron服务
func cronRun(cmd *cobra.Command, _ []string) {
	pidFilePath := viper.GetString("cronServer.pidFilePath")
	pidFilePerm := os.FileMode(viper.GetUint32("cronServer.pidFilePerm"))
	logFilePath := viper.GetString("cronServer.logFilePath")
	isDaemon, _ := cmd.Flags().GetInt("daemon")
	log.SysInfo("cronServer script read to exec",
		zap.String("pidFilePath", pidFilePath),
		zap.Uint32("pidFilePerm", uint32(pidFilePerm)),
		zap.Int("pid", os.Getpid()),
		zap.Int("daemon", isDaemon),
	)

	savePidFile(pidFilePath, os.Getpid(), pidFilePerm)
	// 处理系统信号
	handleExitSignals()

	cronYamlFilePath := viper.GetString("cronServer.cronYamlFilePath")
	if cronYamlFilePath == "" {
		cronYamlFilePath = "./cron.yaml"
	}

	log.FmtPrint(fmt.Sprintf("cronYamlFilePath=%s", cronYamlFilePath))

	registerCronTask(cronYamlFilePath)

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
			log.SysError(fmt.Sprintf("Error starting cron server daemon process:%s", err.Error()))
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

	// 支持使用秒级表达式
	cronTab := cron.New(cron.WithSeconds())
	// 添加cron任务定时记录pid
	_, _ = cronTab.AddFunc("@every 10s", func() {
		savePidFile(pidFilePath, os.Getpid(), pidFilePerm)
	})
	cronTab.Start()
	log.SysInfo("cron server start successful")
	select {}
}

// LoadWithCronYamlFile 加载cron.yaml文件
func LoadWithCronYamlFile(cronYamlFilePath string) (*map[string]*crontab.CronTaskMeta, error) {
	v := viper.New()
	// 配置解析设置
	v.SetConfigFile(cronYamlFilePath) // 直接指定文件路径
	v.SetConfigType("yaml")           // 明确指定类型

	// 读取配置
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("读取cron配置文件cron.yaml失败: %w", err)
	}

	// 解析到目标结构
	var result map[string]*crontab.CronTaskMeta
	if err := v.Unmarshal(&result); err != nil {
		return nil, fmt.Errorf("cron.yaml配置解析失败: %w", err)
	}

	// 验证关键字段
	for key, task := range result {
		if task.UniqueId == "" {
			return nil, fmt.Errorf("cronTask.%s 缺少 UniqueId", key)
		}
		if task.BinFile == "" {
			return nil, fmt.Errorf("cronTask.%s 缺少 BinFile", key)
		}
	}

	return &result, nil
}
