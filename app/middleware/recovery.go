package middleware

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"runtime"

	"github.com/bingcool/gofy/src/log"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// SetGlobalRecovery 设置全局Recovery
func SetGlobalRecovery(router *gin.Engine) {
	router.Use(customRecovery())
}

// CustomRecovery 自定义全局Recovery的响应
func customRecovery() gin.HandlerFunc {
	handleFn := func(c *gin.Context, err any) {
		c.AbortWithStatus(http.StatusOK)
		// 捕获恐慌并获取堆栈信息
		stackRes := stack(3)
		errorMsg := fmt.Sprintf("%s 【stack trace:%s】", err, stackRes)
		response := &Response{
			ReqId: "",
		}
		response.ReturnJson(c, -1, struct{}{}, errorMsg)
	}
	// 捕获恐慌并记录日志
	go func() {
		log.SysError("panic", zap.String("req_id", ""))
	}()
	return gin.RecoveryWithWriter(gin.DefaultErrorWriter, handleFn)
}

var (
	dunno     = []byte("???")
	centerDot = []byte("·")
	dot       = []byte(".")
	slash     = []byte("/")
)

// stack returns a nicely formatted stack frame, skipping skip frames.
func stack(skip int) []byte {
	buf := new(bytes.Buffer) // the returned data
	// As we loop, we open files and read them. These variables record the currently
	// loaded file.
	var lines [][]byte
	var lastFile string
	for i := skip; ; i++ { // Skip the expected number of frames
		pc, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}
		// Print this much at least.  If we can't find the source, it won't show.
		_, _ = fmt.Fprintf(buf, "%s:%d (0x%x)\n", file, line, pc)
		if file != lastFile {
			data, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			lines = bytes.Split(data, []byte{'\n'})
			lastFile = file
		}
		_, _ = fmt.Fprintf(buf, "\t%s: %s\n", function(pc), source(lines, line))
	}
	return buf.Bytes()
}

// source returns a space-trimmed slice of the n'th line.
func source(lines [][]byte, n int) []byte {
	n-- // in stack trace, lines are 1-indexed but our array is 0-indexed
	if n < 0 || n >= len(lines) {
		return dunno
	}
	return bytes.TrimSpace(lines[n])
}

// function returns, if possible, the name of the function containing the PC.
func function(pc uintptr) []byte {
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return dunno
	}
	name := []byte(fn.Name())
	// The name includes the path name to the package, which is unnecessary
	// since the file name is already included.  Plus, it has center dots.
	// That is, we see
	//	runtime/debug.*T·ptrmethod
	// and want
	//	*T.ptrmethod
	// Also the package path might contain dot (e.g. code.google.com/...),
	// so first eliminate the path prefix
	if lastSlash := bytes.LastIndex(name, slash); lastSlash >= 0 {
		name = name[lastSlash+1:]
	}
	if period := bytes.Index(name, dot); period >= 0 {
		name = name[period+1:]
	}
	name = bytes.ReplaceAll(name, centerDot, dot)
	return name
}
