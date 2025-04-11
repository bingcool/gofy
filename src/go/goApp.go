package _go

import (
	"github.com/bingcool/gofy/src/log"
	"go.uber.org/zap"
)

type Context struct {
	BucketMap map[string]any
	Channel   chan any
}

type goFunc func(ctx Context)

func App(callback goFunc, ctx Context) {
	go func() {
		defer func() {
			if err := recover(); err != nil {
				log.SysError("panic", zap.Any("err", err))
			}
		}()
		callback(ctx)
	}()
}
