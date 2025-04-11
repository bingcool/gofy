package request

import (
	"sync"
	"time"

	"github.com/bingcool/gofy/src/log"
	"github.com/natefinch/lumberjack"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	logSyncOnce   sync.Once
	requestLogger *zap.Logger
	currentDate   string
	logSyncMutex  sync.Mutex
)

func init() {
	logSyncOnce.Do(func() {
		currentDate = time.Now().Format("2006-01-02")
		requestLogger = initRequestLogger()
	})
}

// Log 记录请求信息
func Log(msg string, fields ...zap.Field) {
	syncDailyLogger()
	requestLogger.Info(msg, fields...)
}

func initRequestLogger() *zap.Logger {
	errorLogWriter := &lumberjack.Logger{
		Filename:   log.ParseDayLogPath(viper.GetString("requestLogger.error.Filename")), // 日志文件路径
		MaxSize:    viper.GetInt("requestLogger.error.MaxSize"),                          // 单文件最大100MB（非必须，按天切割可设较大值）
		MaxBackups: viper.GetInt("requestLogger.error.MaxBackups"),                       // 保留最近7天的日志
		MaxAge:     viper.GetInt("requestLogger.error.MaxAge"),                           // 保留7天
		Compress:   viper.GetBool("requestLogger.error.MaxAge"),                          // 是否压缩旧日志                               // 是否压缩旧日志
	}

	encoderConfig := log.GetEncoderConfig()
	errorCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(errorLogWriter),
		zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
			return lvl >= zap.ErrorLevel
		}),
	)

	// 构建 Logger
	zapLogger := zap.New(errorCore, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel))
	defer func(zapLogger *zap.Logger) {
		_ = zapLogger.Sync()
	}(zapLogger)

	return zapLogger
}

func syncDailyLogger() {
	today := time.Now().Format("2006-01-02")
	if today != currentDate {
		go func() {
			logSyncMutex.Lock()
			defer logSyncMutex.Unlock()
			if today != currentDate {
				currentDate = today
				initRequestLogger()
			}
		}()
	}
}
