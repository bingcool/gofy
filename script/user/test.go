package user

import (
	"fmt"
	"time"

	"github.com/bingcool/gofy/src/log"
	"github.com/spf13/cobra"
)

// UserFixedCommandName 定义命令名称
const (
	UserFixedCommandName  = "fixed-user"
	UserFixedCommandName1 = "fixed-user1"
)

type UserFixed struct {
}

func NewUserFixed() *UserFixed {
	return &UserFixed{}
}

func (user *UserFixed) Handle(cmd *cobra.Command) {
	log.Info("script test")
	fmt.Println("script test fixed-user")
	time.Sleep(time.Second * 5)
}
