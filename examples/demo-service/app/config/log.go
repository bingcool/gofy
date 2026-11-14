package config

import (
	"path/filepath"

	"github.com/bingcool/gofy/core/systemenv"
)

// applyLogPaths appends per-mode log files to the configured outputs, matching
// swoolefy's cli/script split. Daemon mode writes
// daemon/{date}/info/<worker>.log, daemon/{date}/error/<worker>.log and
// daemon/{date}/error/<worker>-system_error.log, so one worker owns one file
// set; script mode writes the same layout with the script name; HTTP mode
// writes http/{info,error,system_error}.log plus access/access.log, which only
// carries HTTP access entries. {date} is expanded by logger at rotation time.
//
// Paths are appended, not replaced, so outputs such as stdout from config.yaml
// survive; a hot reload that loads the config again appends to the new config.
// applyLogPaths 按执行模式追加日志文件，与 swoolefy 的 cli/script 分目录一致。
// daemon 模式写 daemon/{date}/info/<worker>.log、daemon/{date}/error/<worker>.log
// 与 daemon/{date}/error/<worker>-system_error.log，一个 worker 对应一组文件；
// 脚本模式用同样的布局，只是名字换成脚本名；HTTP 模式写
// http/{info,error,system_error}.log，以及只承载 HTTP 访问日志的
// access/access.log。{date} 由 logger 在滚动时替换。
// 这里追加而不是覆盖，config.yaml 里的 stdout 等输出仍然保留；热更新重新 Load
// 时会再次追加到新配置上。
func applyLogPaths(cfg *Config) {
	if systemenv.IsWorkerService() {
		// Daemon logs are named after the worker that produced them, so a
		// process running one worker writes exactly one file set and the file
		// name answers "which worker is this" without opening the log.
		// daemon 日志按产出它的 worker 命名，只跑一个 worker 的进程因此只写一组
		// 文件，文件名本身就回答了"这是哪个 worker"，不必打开日志去看。
		dateDir := filepath.Join("storage", "log", "daemon", "{date}")
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

// workerLogName picks the file stem for daemon logs: the worker name when the
// process runs exactly one, otherwise the group name. Several names cannot fit
// one file name, and the group is then the honest description of what runs; the
// worker field still tells the lines apart.
// workerLogName 选择 daemon 日志的文件名主干：进程只跑一个 worker 时用它的名字，
// 否则用分组名。多个名字塞不进一个文件名，此时分组名才是对"跑的是什么"的诚实描述，
// 每条日志仍由 worker 字段区分。
func workerLogName() string {
	if names := systemenv.WorkerNames(); len(names) == 1 {
		return names[0]
	}
	if group := systemenv.WorkerGroup(); group != "" {
		return group
	}
	return "default"
}
