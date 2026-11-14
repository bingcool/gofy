package rocketmq

import (
	"os"
	"path/filepath"
	"sync"

	rmq "github.com/apache/rocketmq-clients/golang/v5"
)

// sdkLogRootEnv is the v5 SDK's own environment variable for the root of its
// lumberjack log file. It is a process-wide setting: the SDK keeps one global
// logger, so it cannot be configured per client.
// sdkLogRootEnv 是 v5 SDK 自己读取的日志根目录环境变量。SDK 只有一个全局 logger，
// 因此这是进程级设置，无法按客户端分别配置。
const sdkLogRootEnv = "rocketmq.client.logRoot"

// configureSDKLogRoot keeps the SDK's global logger away from the read-only
// root volume.
//
// v5.1.2 builds its default path as
//
//	os.Getenv("user.home") + "/logs/rocketmqlogs"
//
// "user.home" is Java's system property, not a Go environment variable: in a Go
// process it is empty, so the path becomes /logs/rocketmqlogs/... . The SDK's
// init() constructs that lumberjack writer before main runs, and the first log
// line makes it call MkdirAll("/logs"). On macOS the root volume is read-only,
// so every SDK log line instead produces
//
//	write error: can't make directories for new logfile: mkdir /logs: read-only file system
//
// and the SDK's diagnostics go nowhere. Upstream v5.1.4 fixed the default with
// os.UserHomeDir(); this does the equivalent for the version we pin, without
// pulling in that release's renamed consumer options and behavior changes.
//
// An explicit rocketmq.client.logRoot always wins. Otherwise the SDK logs go
// under the system temp dir, next to the /tmp/rocketmq-logger.log the SDK's
// other logger already uses. ResetLogger rebuilds the writer before anything is
// written: constructing a lumberjack.Logger touches no files, so the bad
// default never gets a chance to create /logs.
//
// configureSDKLogRoot 让 SDK 的全局 logger 远离只读的根卷。
//
// v5.1.2 的默认路径拼成 os.Getenv("user.home") + "/logs/rocketmqlogs"；"user.home"
// 是 Java 系统属性而不是 Go 环境变量，在 Go 进程里为空，路径于是变成
// /logs/rocketmqlogs/... 。SDK 的 init() 在 main 之前就构造好这个 lumberjack writer，
// 第一条日志就会触发 MkdirAll("/logs")。macOS 根卷只读，于是每条 SDK 日志都变成
// "write error: ... mkdir /logs: read-only file system"，而诊断信息本身全部丢失。
// 上游 v5.1.4 用 os.UserHomeDir() 修了默认值；这里对当前锁定的版本做等价处理，
// 不引入那一版改名过的 consumer option 与行为变化。
//
// 显式设置 rocketmq.client.logRoot 时以它为准；否则 SDK 日志写到系统临时目录，
// 与 SDK 另一个 logger 的 /tmp/rocketmq-logger.log 放在一起。ResetLogger 在任何写入
// 之前重建 writer：构造 lumberjack.Logger 不碰文件系统，错误的默认值因此没机会创建 /logs。
func configureSDKLogRoot() string {
	if root := os.Getenv(sdkLogRootEnv); root != "" {
		return root
	}
	root := filepath.Join(os.TempDir(), "rocketmqlogs")
	if err := os.Setenv(sdkLogRootEnv, root); err != nil {
		return ""
	}
	rmq.ResetLogger()
	return root
}

// Public packages in this repository must not use init(), so the repair runs
// lazily on the first public constructor instead. Every SDK log line is
// produced after a client or consumer is built, which makes New / NewConsumer
// early enough; sync.Once keeps a concurrent first use from racing
// ResetLogger.
//
// 本仓库的公开包不允许 init()，因此修复改为在第一个公开构造函数里懒执行。SDK 的每条
// 日志都发生在建好 client 或 consumer 之后，New / NewConsumer 足够早；sync.Once
// 避免并发首次调用时竞态地重置 logger。
var sdkLogOnce sync.Once

func ensureSDKLogRoot() {
	sdkLogOnce.Do(func() { _ = configureSDKLogRoot() })
}
