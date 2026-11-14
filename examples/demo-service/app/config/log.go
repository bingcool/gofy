package config

import (
	"path/filepath"

	"github.com/bingcool/gofy/core/systemenv"
)

// applyLogPaths appends per-mode log files to the configured outputs, matching
// swoolefy's cli/script split. Worker mode writes
// worker/{date}/info/<worker>.log, worker/{date}/error/<worker>.log and
// worker/{date}/error/<worker>-system_error.log: one file set per process, named
// after the single worker it runs or "worker" when it runs a whole group. Script
// mode writes the same layout with the script name; HTTP mode writes
// http/{info,error,system_error}.log plus access/access.log, which only carries
// HTTP access entries. {date} is expanded by logger at rotation time.
//
// Paths are appended, not replaced, so outputs such as stdout from config.yaml
// survive; a hot reload that loads the config again appends to the new config.
// applyLogPaths 按执行模式追加日志文件，与 swoolefy 的 cli/script 分目录一致。
// worker 模式写 worker/{date}/info/<worker>.log、worker/{date}/error/<worker>.log
// 与 worker/{date}/error/<worker>-system_error.log：一个进程一组文件，名字取它运行的
// 那个 worker，整组启动时用 "worker"。脚本模式用同样的布局，只是名字换成脚本名；HTTP
// 模式写 http/{info,error,system_error}.log，以及只承载 HTTP 访问日志的
// access/access.log。{date} 由 logger 在滚动时替换。
// 这里追加而不是覆盖，config.yaml 里的 stdout 等输出仍然保留；热更新重新 Load
// 时会再次追加到新配置上。
func applyLogPaths(cfg *Config) {
	if systemenv.IsWorkerService() {
		// Worker logs are named after the worker that produced them, so a
		// process running one worker writes exactly one file set and the file
		// name answers "which worker is this" without opening the log.
		// worker 日志按产出它的 worker 命名，只跑一个 worker 的进程因此只写一组
		// 文件，文件名本身就回答了"这是哪个 worker"，不必打开日志去看。
		dateDir := filepath.Join("storage", "log", "worker", "{date}")
		name := workerLogName()
		cfg.Log.Outputs = append(cfg.Log.Outputs, filepath.Join(dateDir, "info", name+".log"))
		cfg.Log.ErrorOutputs = append(cfg.Log.ErrorOutputs, filepath.Join(dateDir, "error", name+".log"))
		cfg.Log.SystemErrorOutputs = append(cfg.Log.SystemErrorOutputs, filepath.Join(dateDir, "error", name+"-system_error.log"))
		return
	}
	if systemenv.IsScriptService() {
		dateDir := filepath.Join("storage", "log", "script", "{date}")
		infoName, errorName, systemName := "info.log", "error.log", "system_error.log"
		if name := systemenv.ScriptName(); name != "" {
			infoName = name + ".log"
			errorName = name + ".log"
			systemName = name + "-system_error.log"
		}
		cfg.Log.Outputs = append(cfg.Log.Outputs, filepath.Join(dateDir, "info", infoName))
		cfg.Log.ErrorOutputs = append(cfg.Log.ErrorOutputs, filepath.Join(dateDir, "error", errorName))
		cfg.Log.SystemErrorOutputs = append(cfg.Log.SystemErrorOutputs, filepath.Join(dateDir, "error", systemName))
		return
	}
	dir := filepath.Join("storage", "log", "http")
	cfg.Log.Outputs = append(cfg.Log.Outputs, filepath.Join(dir, "info.log"))
	cfg.Log.ErrorOutputs = append(cfg.Log.ErrorOutputs, filepath.Join(dir, "error.log"))
	cfg.Log.SystemErrorOutputs = append(cfg.Log.SystemErrorOutputs, filepath.Join(dir, "system_error.log"))
	cfg.Log.AccessOutputs = append(cfg.Log.AccessOutputs, filepath.Join("storage", "log", "access", "access.log"))
}

// workerLogName picks the file stem for worker logs: the worker's own name when
// the process runs exactly one, otherwise "worker". The group is deliberately
// not part of the name: every line already carries the group inside the worker
// identity, and a file named after the group says nothing about who wrote it.
// workerLogName 选择 worker 日志的文件名主干：进程只跑一个 worker 时用它的名字，否则
// 用 "worker"。分组刻意不进文件名：每行日志的 worker 字段已经带着分组，而按分组命名
// 说明不了这些行是谁写的。
func workerLogName() string {
	if names := systemenv.WorkerNames(); len(names) == 1 {
		return names[0]
	}
	return "worker"
}
