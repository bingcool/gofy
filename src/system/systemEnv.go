package system

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func IsDev() {

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
