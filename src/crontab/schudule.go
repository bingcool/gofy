package crontab

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/bingcool/gofy/src/log"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

var crontabSchedule *cron.Cron
var CronUniqueIdEntryIDMap map[string]*CronTaskMeta

// init 初始化定时任务
func init() {
	var opts []cron.Option
	opts = append(opts, cron.WithSeconds(), cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)))
	crontabSchedule = cron.New(opts...)
}

// StartScheduleCronTask 启动定时任务
func StartScheduleCronTask(scheduleCronTask *map[string]*CronTaskMeta) {
	runScheduleCronTask(scheduleCronTask)
}

// runScheduleCronTask 运行定时任务
func runScheduleCronTask(scheduleCronTask *map[string]*CronTaskMeta) {
	scheduleCronTaskMap := make(map[string]*CronTaskMeta)
	for _, cronTaskMeta := range *scheduleCronTask {
		express := cronTaskMeta.Express

		cronUniqueId := getUniqueId(cronTaskMeta)
		cronTaskMeta.UniqueId = cronUniqueId
		scheduleCronTaskMap[cronUniqueId] = cronTaskMeta

		// 不存在的定时任务, 新增定时任务
		if _, ok := CronUniqueIdEntryIDMap[cronUniqueId]; !ok {
			EntryID, err := crontabSchedule.AddFunc(express, func() {
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
				fmt.Println("Cron AddFunc error=", err)
				return
			}
			if CronUniqueIdEntryIDMap == nil {
				CronUniqueIdEntryIDMap = make(map[string]*CronTaskMeta)
			}
			cronTaskMeta.EntryID = EntryID
			CronUniqueIdEntryIDMap[cronUniqueId] = cronTaskMeta
			crontabSchedule.Start()
		}
	}

	// 现有的进行中任务与最新配置任务对比，不在最新配置任务中的定时任务则删除
	for cronUniqueId, cronTaskMeta := range CronUniqueIdEntryIDMap {
		if _, ok := scheduleCronTaskMap[cronUniqueId]; !ok {
			crontabSchedule.Remove(cronTaskMeta.EntryID)
			delete(CronUniqueIdEntryIDMap, cronUniqueId)
		}
	}
}

// getUniqueId 获取唯一id
func getUniqueId(cronTaskMeta *CronTaskMeta) string {
	cronUniqueId := cronTaskMeta.UniqueId
	if cronUniqueId == "" {
		cronUniqueId = cronTaskMeta.BinFile + "_" + cronTaskMeta.Express
		if cronTaskMeta.Flags != nil && len(cronTaskMeta.Flags) > 0 {
			flagStr := strings.Join(cronTaskMeta.Flags, "_")
			cronUniqueId = cronUniqueId + "_" + flagStr
		}

		if cronTaskMeta.BetweenDateTime != nil && len(cronTaskMeta.BetweenDateTime) > 0 {
			betweenDateTimeStr := strings.Join(cronTaskMeta.BetweenDateTime, "_")
			cronUniqueId = cronUniqueId + "_" + betweenDateTimeStr
		}

		if cronTaskMeta.SkipDateTime != nil && len(cronTaskMeta.SkipDateTime) > 0 {
			skipDateTimeStr := strings.Join(cronTaskMeta.SkipDateTime, "_")
			cronUniqueId = cronUniqueId + "_" + skipDateTimeStr
		}

		// md5
		hash := md5.Sum([]byte(cronUniqueId))
		cronUniqueId = hex.EncodeToString(hash[:])
	}

	return cronUniqueId
}
