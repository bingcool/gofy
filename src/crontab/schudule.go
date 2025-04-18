package crontab

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/bingcool/gofy/src/log"
	"github.com/bingcool/gofy/src/system"
	"github.com/gogf/gf/v2/util/gutil"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

// StartScheduleCronTask 启动定时任务
func StartScheduleCronTask(scheduleCronTask *map[string]*CronMeta) {
	for commandName, cronMeta := range *scheduleCronTask {
		express := cronMeta.Express
		var opts []cron.Option
		opts = append(opts, cron.WithSeconds(), cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)))

		fmt.Println("commandName=", commandName)
		crontabSchedule := cron.New(opts...)
		_, err := crontabSchedule.AddFunc(express, func() {
			forkScriptProcess(cronMeta)
		})
		if err != nil {
			fmt.Println("cron AddFunc error=", err)
			return
		}

		crontabSchedule.Start()
	}
}

// forkScriptProcess 任务时间到，拉起新的进程处理任务
func forkScriptProcess(cronMeta *CronMeta) {
	if system.IsLinux() || system.IsMacos() {
		newArgs := make([]string, 0)
		binFileItems := strings.Split(cronMeta.BinFile, " ")
		binFile := binFileItems[0]
		newArgs = append(newArgs, binFileItems[1:]...)

		gutil.Dump(newArgs)

		if cronMeta.Flags != nil && len(cronMeta.Flags) > 0 {
			newArgs = append(newArgs, cronMeta.Flags...)
		}
		newArgs = append(newArgs, "--from-flag=cron --daemon=1")
		newCmd := exec.Command(binFile, newArgs...)
		//newCmd.Stdin = os.Stdin
		//newCmd.Stdout = os.Stdout
		//newCmd.Stderr = os.Stderr
		err := newCmd.Start()
		if err != nil {
			_, _ = fmt.Println("forkScriptProcess fork script process failed: ", err.Error())
			log.SysError(fmt.Sprintf("forkScriptProcess fork script process failed: %s", err.Error()))
		}
		// 等待子进程退出并回收进程表编号
		go func() {
			if err1 := newCmd.Wait(); err1 != nil {
				fmt.Printf("子进程退出错误: %v", err1)
				log.SysError(fmt.Sprintf("forkScriptProcess fork script process exit error: %s", err1.Error()), zap.Any("cronMeta", cronMeta), zap.Any("newArgs", newArgs), zap.Any("binFile", binFile))
			} else {
				fmt.Println("子进程正常退出")
				log.SysInfo(fmt.Sprintf("cron fork task script process exit successful --c=%s", cronMeta.BinFile))
			}
		}()
	}
}
