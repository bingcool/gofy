package crontab

import (
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type CronTask struct {
	ID int32 `gorm:"column:id;primaryKey;autoIncrement:true" json:"id"`

	// 任务名称
	Name string `gorm:"column:name;not null;comment:任务名称" json:"name"`

	// 任务唯一标志
	UniqueId string `gorm:"column:unique_id;not null;comment:任务名称" json:"unique_id"`

	// cron表达式
	Expression string `gorm:"column:expression;not null;comment:cron表达式" json:"expression"`

	// 执行命令
	ExecScript string `gorm:"column:exec_script;not null;comment:执行命令" json:"exec_script"`

	// 执行类型 1-shell，2-http
	ExecType int32 `gorm:"column:exec_type;not null;default:1;comment:执行类型 1-shell，2-http" json:"exec_type"`

	// 状态 0-禁用，1-启用
	Status int32 `gorm:"column:status;not null;comment:状态 0-禁用，1-启用" json:"status"`

	// 是否阻塞执行 0-否，1->是
	WithBlockLapping int32 `gorm:"column:with_block_lapping;not null;comment:是否阻塞执行 0-否，1->是" json:"with_block_lapping"`

	// 描述
	Description string `gorm:"column:description;not null;comment:描述" json:"description"`

	// 允许执行时间段
	CronBetween *datatypes.JSON `gorm:"omitempty;column:cron_between;comment:允许执行时间段" json:"cron_between"`

	// 不允许执行时间段(即需跳过的时间段)
	CronSkip *datatypes.JSON `gorm:"omitempty;column:cron_skip;comment:不允许执行时间段(即需跳过的时间段)" json:"cron_skip"`

	// http请求方法
	HTTPMethod string `gorm:"column:http_method;not null;comment:http请求方法" json:"http_method"`

	// http请求体
	HTTPBody *datatypes.JSON `gorm:"omitempty;column:http_body;comment:http请求体" json:"http_body"`

	// http请求头
	HTTPHeaders *datatypes.JSON `gorm:"omitempty;column:http_headers;comment:http请求头" json:"http_headers"`

	// http请求超时时间，单位：秒
	HTTPRequestTimeOut int32 `gorm:"column:http_request_time_out;not null;comment:http请求超时时间，单位：秒" json:"http_request_time_out"`

	// 创建时间
	CreatedAt time.Time `gorm:"column:created_at;not null;default:CURRENT_TIMESTAMP;comment:创建时间" json:"created_at"`

	// 修改时间
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:CURRENT_TIMESTAMP;comment:修改时间" json:"updated_at"`

	// 删除时间
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;comment:删除时间" json:"deleted_at"`
}
