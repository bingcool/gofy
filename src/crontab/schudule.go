package crontab

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/bingcool/gofy/src/log"
	"github.com/robfig/cron/v3"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var crontabSchedule *cron.Cron
var CronUniqueIdEntryIDMap map[string]*CronTaskMeta

// init 初始化定时任务
func init() {
	var opts []cron.Option
	opts = append(opts, cron.WithSeconds(), cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)))
	crontabSchedule = cron.New(opts...)
	if CronUniqueIdEntryIDMap == nil {
		CronUniqueIdEntryIDMap = make(map[string]*CronTaskMeta)
	}
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

// LoadWithCronTaskDatabase 加载db的cronTask配置
func LoadWithCronTaskDatabase() (*map[string]*CronTaskMeta, error) {
	cronTaskDatabaseDns := viper.GetString("cronServer.cronTaskDatabaseDns")

	if cronTaskDatabaseDns == "" {
		return nil, fmt.Errorf("读取cronTaskDatabaseDns的配置项失败")
	}

	//db := initDb(cronTaskDatabaseDns)

	// 解析到目标结构
	var result map[string]*CronTaskMeta

	return &result, nil
}

// LoadWithCronTaskYamlFile 加载cron.yaml文件
func LoadWithCronTaskYamlFile(cronYamlFilePath string) (*map[string]*CronTaskMeta, error) {
	v := viper.New()
	// 配置解析设置
	v.SetConfigFile(cronYamlFilePath)
	v.SetConfigType("yaml")

	// 读取配置
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("读取cron配置文件cron.yaml失败: %w", err)
	}

	// 解析到目标结构
	var result map[string]*CronTaskMeta
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

func initDb(dsn string) *gorm.DB {
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		// 处理数据库连接错误
		panic(err)
	}

	// 设置连接池大小
	sqlDB, err := db.DB()
	if err != nil {
		panic(err)
	} else {
		sqlDB.SetMaxIdleConns(3)
		sqlDB.SetMaxOpenConns(5)
	}

	return db
}
