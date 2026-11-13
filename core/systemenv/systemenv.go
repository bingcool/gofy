// Package systemenv provides read-only checks of the environment, the OS and
// goroutines.
// The environment value comes from GOFY_ENV and falls back to dev when unset or
// empty. Every function is stateless: it reads the current process state on each
// call, caches nothing and writes no globals, which keeps it testable and safe to
// switch at runtime.
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
	"unicode"
)

// Environment constants, one per GOFY_ENV value.
// 环境常量，与 GOFY_ENV 的取值一一对应。
const (
	// EnvDev is the development environment.
	// EnvDev 开发环境。
	EnvDev = "dev"
	// EnvTest is the test environment.
	// EnvTest 测试环境。
	EnvTest = "test"
	// EnvGrad is the canary (gradual rollout) environment.
	// EnvGrad 灰度环境。
	EnvGrad = "grad"
	// EnvProd is the production environment.
	// EnvProd 生产环境。
	EnvProd = "prod"

	envKey = "GOFY_ENV"
)

// GetEnv returns the current environment: GOFY_ENV trimmed and lowercased, or dev
// when it is unset or empty.
// GetEnv 返回当前环境：GOFY_ENV 去空格并转小写后的值，未设置时返回 dev。
func GetEnv() string {
	env := strings.ToLower(strings.TrimSpace(os.Getenv(envKey)))
	if env == "" {
		return EnvDev
	}
	return env
}

// IsDev reports whether the current environment is development.
// IsDev 报告当前是否是开发环境。
func IsDev() bool { return GetEnv() == EnvDev }

// IsTest reports whether the current environment is test.
// IsTest 报告当前是否是测试环境。
func IsTest() bool { return GetEnv() == EnvTest }

// IsGrad reports whether the current environment is canary.
// IsGrad 报告当前是否是灰度环境。
func IsGrad() bool { return GetEnv() == EnvGrad }

// IsProd reports whether the current environment is production.
// IsProd 报告当前是否是生产环境。
func IsProd() bool { return GetEnv() == EnvProd }

// IsWindows reports whether the current OS is Windows.
// IsWindows 报告当前运行系统是否是 Windows。
func IsWindows() bool { return runtime.GOOS == "windows" }

// IsLinux reports whether the current OS is Linux.
// IsLinux 报告当前运行系统是否是 Linux。
func IsLinux() bool { return runtime.GOOS == "linux" }

// IsMacos reports whether the current OS is macOS.
// IsMacos 报告当前运行系统是否是 macOS。
func IsMacos() bool { return runtime.GOOS == "darwin" }

// IsHTTPService reports whether the current process serves HTTP.
// It holds when the first non-flag argument is start, stop or restart, or when there
// is no subcommand at all (gofy starts the HTTP service by default): restart stops
// the old instance and starts in place, stop stops the HTTP instance through the pid
// file. Commands such as version and help are neither an HTTP service nor script
// mode, so both Is*Service functions return false for them.
// IsHTTPService 报告当前进程是否在操作 HTTP 服务。
//
// 成立条件是入口参数的第一个非 flag 值为 start、stop、restart，或者没有子命令
// （gofy 默认启动 HTTP 服务）：restart 会先停旧实例再就地启动，stop 通过 pid
// 文件停止 HTTP 实例。version、help 等命令既不是 HTTP 服务，也不是脚本模式，
// 两个 Is*Service 都返回 false。
func IsHTTPService() bool {
	name, ok := subcommand(os.Args[1:])
	if !ok {
		return false
	}
	switch name {
	case "", "start", "stop", "restart":
		return true
	default:
		return false
	}
}

// IsScriptService reports whether the current process runs a maintenance script
// through the script subcommand, for example
// `demo-service script fix-order --order-id=1`.
// IsScriptService 报告当前进程是否通过 script 子命令运行维护脚本，
// 例如 `demo-service script fix-order --order-id=1`。
func IsScriptService() bool {
	name, ok := subcommand(os.Args[1:])
	return ok && name == "script"
}

// ScriptName returns the script name that follows the script subcommand, so logs can
// be separated per script.
// For example `demo-service script fix-order --order-id=1` returns "fix-order". The
// result is stripped of spaces and keeps only letters, digits, '-' and '_', so
// command-line arguments never end up in a file path; with no script argument (such
// as script --list or script --help) it returns an empty string. Like the rest of the
// package it is stateless.
// ScriptName 返回 script 子命令后面的脚本名，用于按脚本区分日志文件。
//
// 例如 `demo-service script fix-order --order-id=1` 返回 "fix-order"。
// 返回值会去掉空白字符，只保留字母、数字、'-' 和 '_'，避免把命令行参数
// 直接拼进文件路径；没有任何脚本参数（如 script --list、script --help）
// 时返回空字符串。该函数与包内其他函数一样无状态。
func ScriptName() string {
	args := os.Args[1:]
	index := -1
	for i, arg := range args {
		if strings.HasPrefix(arg, "-") {
			if arg == "-h" || arg == "--help" {
				return ""
			}
			continue
		}
		if arg != "script" {
			return ""
		}
		index = i
		break
	}
	if index < 0 {
		return ""
	}
	for _, arg := range args[index+1:] {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return sanitizeScriptName(arg)
	}
	return ""
}

func sanitizeScriptName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsSpace(r):
			// Drop whitespace so the log file name never contains a space.
			// 去掉空白，保证日志文件名不包含空格。
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// subcommand returns the first non-flag argument. With no subcommand it returns
// ("", true), meaning the root command starts the service by default; with only
// -h/--help it returns false so a help call is not mistaken for an HTTP start.
// subcommand 返回第一个非 flag 参数。没有子命令时返回 ("", true)，表示走根命令
// 的默认启动；只带 -h/--help 时返回 false，避免把帮助调用误判为 HTTP 启动。
func subcommand(args []string) (string, bool) {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			if arg == "-h" || arg == "--help" {
				return "", false
			}
			continue
		}
		return arg, true
	}
	return "", true
}

// GetGoroutineID returns the ID of the current goroutine.
// The runtime does not expose it, so the first line of runtime.Stack
// ("goroutine 123 [running]:") is parsed. It returns 0 when parsing fails; the value
// only suits logging and debugging, never synchronization or cache keys, because
// goroutine IDs are reused.
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
