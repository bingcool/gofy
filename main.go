package main

import (
	"github.com/bingcool/gofy/src/cmd"
	_ "github.com/bingcool/gofy/src/log"
	_ "go.uber.org/automaxprocs"
)

func main() {
	cmd.Execute()
}
