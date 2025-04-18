package timelocal

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

// SetTimezone 设置时区
func SetTimezone() {

	timezone := viper.GetString("date.timezone")
	if timezone == "" {
		timezone = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		panic(fmt.Sprintf("加载时区失败: %v\n", err))
	}
	// 设置全局时区
	time.Local = loc
}
