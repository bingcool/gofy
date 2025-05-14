package entity

import "github.com/bingcool/gofy/app/dao/model"

type CronTaskLog struct {
	model.CronTaskLog
}

func NewCronTaskLog() *CronTaskLog {
	return &CronTaskLog{}
}
