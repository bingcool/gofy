package crontab

import "time"

type CronMeta struct {
	BinFile         string
	Express         string   // cron表达式
	Flags           []string // 参数标识--id=1
	Desc            string   // 描述
	BetweenDateTime []string //只能在某个时间段执行
	SkipDateTime    []string // 跳过某个时间段执行
}

type BetweenDateTime struct {
	StartDateTime time.Time
	EndDateTime   time.Time
}
