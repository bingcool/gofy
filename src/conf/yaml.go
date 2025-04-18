package conf

import (
	"fmt"
	"sync"

	_ "github.com/bingcool/gofy/src/cmd/command"
	"github.com/bingcool/gofy/src/timelocal"
	"github.com/spf13/viper"
)

var yamlSyncOnce sync.Once

func init() {
	LoadYaml()
	timelocal.SetTimezone()
}

func LoadYaml() {
	yamlSyncOnce.Do(func() {
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
		if err := viper.ReadInConfig(); err != nil {
			panic(fmt.Errorf("Fatal error config file: %w", err))
		}
	})
}
