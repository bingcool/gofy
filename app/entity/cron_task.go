package entity

import (
	"github.com/bingcool/gofy/app/dao/model"
)

type CronTask struct {
	model.CronTask
}

type CronTaskEntity struct {
	model.CronTask
	CronSkip    [][]string   `gorm:"omitempty;column:cron_skip;comment:不允许执行时间段(即需跳过的时间段)" json:"cron_skip"`
	HTTPHeaders *HttpHeaders `gorm:"omitempty;column:http_headers;comment:http请求头" json:"http_headers"`
}

type HttpHeaders struct {
	Xyz   string `json:"xyz"`
	Token string `json:"token"`
}

func NewCronTaskEntity() *CronTaskEntity {
	return &CronTaskEntity{}
}
