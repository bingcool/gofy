package crontab

import (
	"fmt"

	"github.com/bingcool/gofy/src/log"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

// StartScheduleCronTask 启动定时任务
func StartScheduleCronTask(scheduleCronTask *map[string]*CronTaskMeta) {
	for _, cronTaskMeta := range *scheduleCronTask {
		express := cronTaskMeta.Express
		var opts []cron.Option
		opts = append(opts, cron.WithSeconds(), cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)))

		crontabSchedule := cron.New(opts...)
		_, err := crontabSchedule.AddFunc(express, func() {
			go func() {
				defer func() {
					if err := recover(); err != nil {
						log.SysError(fmt.Sprintf("cronMeta panic error: %s", err.(error).Error()),
							zap.Any("cronMeta", cronTaskMeta))
					}
				}()

				err := cronTaskMeta.BeforeHandle()
				if err != nil {
					log.SysError(fmt.Sprintf("cronMeta BeforeHandle error: %s", err.Error()),
						zap.Any("cronMeta", cronTaskMeta))
					return
				}
				cronTaskMeta.Exec()
				cronTaskMeta.AfterHandle()
			}()
		})

		if err != nil {
			fmt.Println("cron AddFunc error=", err)
			return
		}

		crontabSchedule.Start()
	}
}
