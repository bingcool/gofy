// Package systemenv 提供环境、运行系统和 goroutine 的只读判定。
//
// 环境值读取 GOFY_ENV，未设置或为空时按 dev 处理。包内所有函数都是无状态的：
// 每次调用读取当前进程状态，不缓存、不写全局变量，便于测试和运行时切换。
package systemenv

import (
	"bytes"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// 环境常量，与 GOFY_ENV 的取值一一对应。
const (
	// EnvDev 开发环境。
	EnvDev = "dev"
	// EnvTest 测试环境。
	EnvTest = "test"
	// EnvGrad 灰度环境。
	EnvGrad = "grad"
	// EnvProd 生产环境。
	EnvProd = "prod"

	envKey = "GOFY_ENV"
)

// GetEnv 返回当前环境：GOFY_ENV 去空格并转小写后的值，未设置时返回 dev。
func GetEnv() string {
	env := strings.ToLower(strings.TrimSpace(os.Getenv(envKey)))
	if env == "" {
		return EnvDev
	}
	return env
}

// IsDev 报告当前是否是开发环境。
func IsDev() bool { return GetEnv() == EnvDev }

// IsTest 报告当前是否是测试环境。
func IsTest() bool { return GetEnv() == EnvTest }

// IsGrad 报告当前是否是灰度环境。
func IsGrad() bool { return GetEnv() == EnvGrad }

// IsProd 报告当前是否是生产环境。
func IsProd() bool { return GetEnv() == EnvProd }

// IsWindows 报告当前运行系统是否是 Windows。
func IsWindows() bool { return runtime.GOOS == "windows" }

// IsLinux 报告当前运行系统是否是 Linux。
func IsLinux() bool { return runtime.GOOS == "linux" }

// IsMacos 报告当前运行系统是否是 macOS。
func IsMacos() bool { return runtime.GOOS == "darwin" }

// GetGoroutineID 返回当前 goroutine 的 ID。
//
// runtime 没有公开该值，这里解析 runtime.Stack 输出的第一行
// （"goroutine 123 [running]:"）。解析失败时返回 0；该值只适合日志和调试，
// 不要用它做同步或缓存键，goroutine ID 会被复用。
func GetGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	line := buf[:n]

	const prefix = "goroutine "
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return 0
	}
	rest := line[len(prefix):]
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return 0
	}
	id, err := strconv.ParseUint(string(rest[:digits]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}
