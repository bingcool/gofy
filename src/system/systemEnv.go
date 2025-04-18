package system

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/viper"
)

const DEV_ENV = "dev"
const TEST_ENV = "test"
const GRAD_ENV = "grad"
const PROD_ENV = "prod"

var env string
var RunModel string

// init 初始化
func init() {
	env = os.Getenv("GOFY_ENV")
	if env == "" {
		panic("GOFY_ENV is not set!!!")
	}
}

// GetEnv 获取当前环境
func GetEnv() string {
	return env
}

// IsDev 判断是否是开发环境
func IsDev() bool {
	if env == DEV_ENV {
		return true
	}
	return false
}

// IsTest 判断是否是测试环境
func IsTest() bool {
	if env == TEST_ENV {
		return true
	}
	return false
}

// IsGrad 判断是否是灰度环境
func IsGrad() bool {
	if env == GRAD_ENV {
		return true
	}
	return false
}

// IsProd 判断是否是生产环境
func IsProd() bool {
	if env == PROD_ENV {
		return true
	}
	return false
}

// IsCliService 判断是否是命令行http服务
func IsCliService() bool {
	if RunModel == "http" {
		return true
	}
	return false
}

// IsHttpService 判断是否是命令行http服务
func IsHttpService() bool {
	return IsCliService()
}

// IsDaemonService 判断是否是守护进程服务
func IsDaemonService() bool {
	if RunModel == "daemon" {
		return true
	}
	return false
}

// IsCronService 判断是否是定时任务服务
func IsCronService() bool {
	if RunModel == "cron" {
		return true
	}
	return false
}

// IsScriptService 判断是否是脚本服务
func IsScriptService() bool {
	if RunModel == "script" {
		return true
	}
	return false
}

// IsWorkerService 判断是否是worker模式服务
func IsWorkerService() bool {
	if IsDaemonService() || IsCronService() || IsScriptService() {
		return true
	}
	return false
}

// IsWindows 判断是否是windows系统
func IsWindows() bool {
	osName := runtime.GOOS
	switch osName {
	case "windows":
		return true
	default:
		return false
	}
}

// IsLinux 判断是否是linux系统
func IsLinux() bool {
	osName := runtime.GOOS
	switch osName {
	case "linux":
		return true
	default:
		return false
	}
}

// IsMacos 判断是否是macos系统
func IsMacos() bool {
	osName := runtime.GOOS
	switch osName {
	case "darwin":
		return true
	default:
		return false
	}
}

// GetStartRootPath 获取启动根目录
func GetStartRootPath() string {
	var startRootPath string
	if IsWindows() {
		startRootPath = viper.GetString("windows_start_root")
	} else if IsLinux() {
		startRootPath = viper.GetString("linux_start_root")
	} else if IsMacos() {
		startRootPath = viper.GetString("macos_start_root")
	} else {
		startRootPath, _ = os.Getwd()
	}
	return startRootPath
}

// GetAppRoot 获取应用根目录
func GetAppRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err.Error())
	}
	parts := []string{dir, "app"}
	return filepath.Join(parts...)
}

// GetConfigRoot 获取配置文件目录
func GetConfigRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err.Error())
	}
	parts := []string{dir, "app", "config"}
	return filepath.Join(parts...)
}

// GetGoroutineID 获取当前协程ID
func GetGoroutineID() uint64 {
	defer func() {
		if err := recover(); err != nil {
			fmt.Println("Failed to get goroutine ID:", err)
		}
	}()

	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	idStr := string(buf[:n])

	var id uint64
	_, err := fmt.Sscanf(idStr, "goroutine %d", &id)
	if err != nil {
		fmt.Println("Failed to parse goroutine ID:", err)
		return 0
	}

	return id
}
