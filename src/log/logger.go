package log

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bingcool/gofy/src/conf"
	"github.com/bingcool/gofy/src/system"
	"github.com/natefinch/lumberjack"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	logSyncOnce  sync.Once
	logger       *zap.Logger
	systemLogger *zap.Logger
	currentDate  string
	logSyncMutex sync.Mutex
)

func init() {
	conf.LoadYaml()
	logSyncOnce.Do(func() {
		currentDate = time.Now().Format("2006-01-02")
		logger = initLogger()
		systemLogger = initSystemLogger()
	})
}

func initLogger() *zap.Logger {
	// 配置日志切割
	logInfoFileName := ParseDayLogPath(viper.GetString("logger.info.Filename"))
	logInfoFileName = parseLogFileName(logInfoFileName)

	infoLogWriter := &lumberjack.Logger{
		Filename:   logInfoFileName,                        // 日志文件路径
		MaxSize:    viper.GetInt("logger.info.MaxSize"),    // 单文件最大100MB（非必须，按天切割可设较大值）
		MaxBackups: viper.GetInt("logger.info.MaxBackups"), // 保留最近7天的日志
		MaxAge:     viper.GetInt("logger.info.MaxAge"),     // 保留7天
		Compress:   viper.GetBool("logger.info.MaxAge"),    // 是否压缩旧日志
	}

	logErrorFileName := ParseDayLogPath(viper.GetString("logger.info.Filename"))
	logErrorFileName = parseLogFileName(logErrorFileName)
	// 配置日志切割
	errorLogWriter := &lumberjack.Logger{
		Filename:   logErrorFileName,                        // 日志文件路径
		MaxSize:    viper.GetInt("logger.error.MaxSize"),    // 单文件最大100MB（非必须，按天切割可设较大值）
		MaxBackups: viper.GetInt("logger.error.MaxBackups"), // 保留最近7天的日志
		MaxAge:     viper.GetInt("logger.error.MaxAge"),     // 保留7天
		Compress:   viper.GetBool("logger.error.MaxAge"),    // 是否压缩旧日志
	}

	// 配置 Zap Encoder（JSON格式）
	encoderConfig := GetEncoderConfig()
	// 创建日志核心（Core）
	infoCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(infoLogWriter), // 绑定日志切割后的 Writer
		zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
			return lvl >= zap.InfoLevel && lvl < zap.ErrorLevel
		}),
	)

	errorCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(errorLogWriter),
		zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
			return lvl >= zap.ErrorLevel
		}),
	)

	// 合并多个 Core
	core := zapcore.NewTee(infoCore, errorCore)
	// 构建 Logger
	zapLogger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel))
	defer func(zapLogger *zap.Logger) {
		_ = zapLogger.Sync()
	}(zapLogger)

	return zapLogger
}

// Info 记录普通信息
func Info(msg string, fields ...zap.Field) {
	syncDailyLogger()
	logger.Info(msg, fields...)
}

// Error 记录错误信息
func Error(msg string, fields ...zap.Field) {
	syncDailyLogger()
	logger.Error(msg, fields...)
}

// parseLogFileName 解析日志文件名
func parseLogFileName(logErrorFileName string) string {
	if system.IsHttpService() {
		return logErrorFileName
	}
	if system.IsDaemonService() {
		logErrorFileName = strings.Replace(logErrorFileName, "app", "daemon", 1)
	} else if system.IsCronService() {
		logErrorFileName = strings.Replace(logErrorFileName, "app", "cron", 1)
	} else if system.IsScriptService() {
		logErrorFileName = strings.Replace(logErrorFileName, "app", "script", 1)
	}

	return logErrorFileName
}

// initSystemLogger 初始化系统日志
func initSystemLogger() *zap.Logger {
	logInfoFileName := ParseDayLogPath(viper.GetString("systemLogger.info.Filename"))
	if system.IsDaemonService() {
		logInfoFileName = strings.Replace(logInfoFileName, "app", "daemon", 1)
	} else if system.IsCronService() {
		logInfoFileName = strings.Replace(logInfoFileName, "app", "cron", 1)
	} else if system.IsScriptService() {
		logInfoFileName = strings.Replace(logInfoFileName, "app", "script", 1)
	}

	infoLogWriter := &lumberjack.Logger{
		Filename:   logInfoFileName,                              // 日志文件路径
		MaxSize:    viper.GetInt("systemLogger.info.MaxSize"),    // 单文件最大100MB（非必须，按天切割可设较大值）
		MaxBackups: viper.GetInt("systemLogger.info.MaxBackups"), // 保留最近7天的日志
		MaxAge:     viper.GetInt("systemLogger.info.MaxAge"),     // 保留7天
		Compress:   viper.GetBool("systemLogger.info.MaxAge"),    // 是否压缩旧日志                               // 是否压缩旧日志
	}

	logErrorFileName := ParseDayLogPath(viper.GetString("systemLogger.error.Filename"))
	logErrorFileName = parseLogFileName(logErrorFileName)

	errorLogWriter := &lumberjack.Logger{
		Filename:   logErrorFileName,                              // 日志文件路径
		MaxSize:    viper.GetInt("systemLogger.error.MaxSize"),    // 单文件最大100MB（非必须，按天切割可设较大值）
		MaxBackups: viper.GetInt("systemLogger.error.MaxBackups"), // 保留最近7天的日志
		MaxAge:     viper.GetInt("systemLogger.error.MaxAge"),     // 保留7天
		Compress:   viper.GetBool("systemLogger.error.MaxAge"),    // 是否压缩旧日志                               // 是否压缩旧日志
	}

	encoderConfig := GetEncoderConfig()

	infoCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(infoLogWriter), // 绑定日志切割后的 Writer
		zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
			return lvl >= zap.InfoLevel && lvl < zap.ErrorLevel
		}),
	)

	errorCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(errorLogWriter),
		zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
			return lvl >= zap.ErrorLevel
		}),
	)

	// 合并多个 Core
	core := zapcore.NewTee(infoCore, errorCore)
	// 构建 Logger
	zapLogger := zap.New(core, zap.AddCaller())
	defer func(zapLogger *zap.Logger) {
		_ = zapLogger.Sync()
	}(zapLogger)

	return zapLogger
}

// SysInfo 记录系统级别的panic错误
func SysInfo(msg string, fields ...zap.Field) {
	syncDailyLogger()
	systemLogger.Info(msg, fields...)
}

// SysError 记录系统级别的panic错误
func SysError(msg string, fields ...zap.Field) {
	syncDailyLogger()
	systemLogger.Error(msg, fields...)
}

// GetEncoderConfig 获取 Zap Encoder 的配置
func GetEncoderConfig() zapcore.EncoderConfig {
	// 配置 Zap Encoder（JSON格式）
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,                      // 小写级别（如 info）
		EncodeTime:     zapcore.TimeEncoderOfLayout("2006-01-02 15:04:05"), // ISO8601时间格式
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder, // 短调用者信息
	}

	return encoderConfig
}

// ParseDayLogPath 日志路径添加日期
func ParseDayLogPath(filePath string) string {
	dir := filepath.Dir(filePath)
	fileName := filepath.Base(filePath)
	day := time.Now().Format("2006-01-02")
	parts := []string{dir, day, fileName}
	filePath = filepath.Join(parts...)
	return filePath
}

// syncDailyLogger 同步动态日期日志
func syncDailyLogger() {
	today := time.Now().Format("2006-01-02")
	if today != currentDate {
		go func() {
			logSyncMutex.Lock()
			defer logSyncMutex.Unlock()
			if today != currentDate {
				currentDate = today
				initLogger()
				initSystemLogger()
			}
		}()
	}
}

// FmtPrint 格式化打印日志
func FmtPrint(message string) {
	fmt.Println(fmt.Sprintf("[%s] %s: %s", "gofy", time.Now().Format("2006-01-02 15:04:05"), message))
}
