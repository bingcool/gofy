package crontab

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bingcool/gofy/src/log"
	"github.com/bingcool/gofy/src/system"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

type CronMetaHandle interface {
	BeforeHandle() error
	Exec()
	AfterHandle()
}

type CronTaskMeta struct {
	UniqueId        string       `yaml:"UniqueId"`        // 唯一id，标志本次定时任务的唯一id
	BinFile         string       `yaml:"BinFile"`         // 二进制执行文件
	Express         string       `yaml:"Express"`         // cron表达式
	Flags           []string     `yaml:"Flags"`           // 参数标识--id=1
	Desc            string       `yaml:"Desc"`            // 描述
	BetweenDateTime []string     `yaml:"BetweenDateTime"` //只能在某个时间段执行
	SkipDateTime    []string     `yaml:"SkipDateTime"`    // 跳过某个时间段执行
	EntryID         cron.EntryID `yaml:"-"`               // cron任务id
}

// BeforeHandle 执行前处理
func (cronTask *CronTaskMeta) BeforeHandle() error {
	// 过滤Between时间段
	err := cronTask.filterBetweenDateTime()
	if err != nil {
		return err
	}

	// 过滤Skip时间段
	err = cronTask.filterSkipDateTime()
	if err != nil {
		return err
	}

	return nil
}

// Exec 执行定时任务
func (cronTask *CronTaskMeta) Exec() {
	if system.IsLinux() || system.IsMacos() {
		newArgs := make([]string, 0)
		binFileItems := strings.Split(cronTask.BinFile, " ")
		binFile := binFileItems[0]
		newArgs = append(newArgs, binFileItems[1:]...)

		if cronTask.Flags != nil && len(cronTask.Flags) > 0 {
			newArgs = append(newArgs, cronTask.Flags...)
		}

		newArgs = append(newArgs, "--from-flag=cron")
		newCmd := exec.Command(binFile, newArgs...)

		execCommand := fmt.Sprintf("%s %s", binFile, strings.Join(newArgs, " "))
		log.SysInfo("执行脚本：" + execCommand)

		newCmd.Stdin = os.Stdin
		newCmd.Stdout = os.Stdout
		newCmd.Stderr = os.Stderr
		err := newCmd.Start()
		if err != nil {
			_, _ = fmt.Println("forkScriptProcess fork script process failed: ", err.Error())
			log.SysError(fmt.Sprintf("forkScriptProcess fork script process failed: %s", err.Error()))
		}
		// wait gc children process
		go func() {
			defer func() {
				newCmd = nil
			}()
			if err1 := newCmd.Wait(); err1 != nil {
				fmt.Printf("子进程退出错误: %v", err1)
				log.SysError(fmt.Sprintf("forkScriptProcess fork script process exit error: %s", err1.Error()),
					zap.Any("binFile", binFile),
					zap.Any("cronMeta", cronTask),
					zap.Any("newArgs", newArgs))
			} else {
				fmt.Println("子进程正常退出")
				log.SysInfo(fmt.Sprintf("cron fork task script process exit successful --c=%s",
					cronTask.BinFile),
					zap.Any("cronMeta", cronTask),
				)
			}
		}()
	}
}

// AfterHandle 执行后处理
func (cronTask *CronTaskMeta) AfterHandle() {

}

// filterBetweenDateTime 过滤Between时间段
func (cronTask *CronTaskMeta) filterBetweenDateTime() error {
	nowTime, ts1, ts2, err := parseTime("BetweenDateTime", cronTask.BetweenDateTime)
	if err != nil {
		return err
	}

	if ts1 <= nowTime && nowTime <= ts2 {
		return nil
	}

	return errors.New(fmt.Sprintf(" Now time[%s] not in betweenDateTime=[%s,%s]",
		time.Now().Format("2006-01-02 15:04:05"),
		time.Unix(ts1, 0).Format("2006-01-02 15:04:05"),
		time.Unix(ts2, 0).Format("2006-01-02 15:04:05"),
	))
}

// filterSkipDateTime 过滤Skip时间段
func (cronTask *CronTaskMeta) filterSkipDateTime() error {
	nowTime, ts1, ts2, err := parseTime("SkipDateTime", cronTask.SkipDateTime)
	if err != nil {
		return err
	}

	// 跳过某些阶段
	if nowTime <= ts1 || nowTime >= ts2 {
		return nil
	}

	return errors.New(fmt.Sprintf(" Now time[%s] not in SkipDateTime=[%s,%s]",
		time.Now().Format("2006-01-02 15:04:05"),
		time.Unix(ts1, 0).Format("2006-01-02 15:04:05"),
		time.Unix(ts2, 0).Format("2006-01-02 15:04:05"),
	))
}

func parseTime(timeField string, timeItems []string) (nowTime int64, startTime int64, endTime int64, err error) {
	num := len(timeItems)
	if timeItems == nil || num == 0 {
		return 0, 0, 0, nil
	}
	var startDateTime string
	var endDateTime string
	if num == 1 {
		startDateTime = timeItems[0]
		if !isValidTimeFormat(startDateTime) {
			return 0, 0, 0, errors.New(fmt.Sprintf("%s=[%s] time format error", timeField, startDateTime))
		}

		endDateTime = time.Now().Format("2006-01-02 15:04:05")
	}

	if num >= 2 {
		startDateTime = timeItems[0]
		endDateTime = timeItems[1]
		if !isValidTimeFormat(startDateTime) || !isValidTimeFormat(endDateTime) {
			return 0, 0, 0, errors.New(fmt.Sprintf("%s=[%s,%s] time format error", timeField, startDateTime, endDateTime))
		}
	}

	t1, err1 := time.Parse(time.DateTime, startDateTime)
	if err1 != nil {
		return 0, 0, 0, err1
	}

	t2, err2 := time.Parse(time.DateTime, endDateTime)

	if err2 != nil {
		return 0, 0, 0, err2
	}
	// 转换为时间戳
	now := time.Now().Unix()
	ts1 := t1.Unix()
	ts2 := t2.Unix()
	return now, ts1, ts2, nil
}

// isValidTimeFormat 判断时间格式是否正确
func isValidTimeFormat(s string) bool {
	const targetLayout = "2006-01-02 15:04:05"
	_, err := time.Parse(targetLayout, s)
	return err == nil
}
