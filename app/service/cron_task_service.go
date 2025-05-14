package service

import (
	"github.com/bingcool/gofy/app/entity"
	"github.com/bingcool/gofy/app/repository"
)

type CronTaskService struct {
}

// NewCronTaskService 创建CronTaskService
func NewCronTaskService() *CronTaskService {
	return &CronTaskService{}
}

// GetCronTaskList 获取任务列表
func (s *CronTaskService) GetCronTaskList() []*entity.CronTask {
	list := repository.NewCronTaskRepos().List(nil, []int32{1, 2, 3})
	return list
}
