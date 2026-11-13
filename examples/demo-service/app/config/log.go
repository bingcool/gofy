package config

import (
	"path/filepath"

	"github.com/bingcool/gofy/core/systemenv"
)

// applyLogPaths appends per-mode log files to the configured outputs, matching
// swoolefy's cli/script split. Script mode writes
// script/{date}/info/<script>.log and script/{date}/error/<script>.log, one
// file set per script; worker mode writes worker/{date}/<group>/{info,error,
// system_error}.log and keeps the worker name as a log field instead of a file
// name; HTTP mode writes http/{info,error,system_error}.log plus
// access/access.log, which only carries HTTP access entries. {date} is expanded
// by logger at rotation time.
//
// Paths are appended, not replaced, so outputs such as stdout from config.yaml
// survive; a hot reload that loads the config again appends to the new config.
// applyLogPaths 按执行模式追加日志文件，与 swoolefy 的 cli/script 分目录一致。
// 脚本模式写 script/{date}/info/<脚本>.log 与 script/{date}/error/<脚本>.log，
// 一个脚本对应一组文件；worker 模式写 worker/{date}/<分组>/{info,error,
// system_error}.log，worker 名作为日志字段而不是文件名；HTTP 模式写
// http/{info,error,system_error}.log，以及只承载 HTTP 访问日志的
// access/access.log。{date} 由 logger 在滚动时替换。
// 这里追加而不是覆盖，config.yaml 里的 stdout 等输出仍然保留；热更新重新 Load
// 时会再次追加到新配置上。
func applyLogPaths(cfg *Config) {
	if systemenv.IsWorkerService() {
		// One directory per group: the worker name is a log field, so a group's
		// files stay together and a pod writes a predictable set of files.
		// 每个分组一个目录：worker 名作为日志字段输出，分组的文件因此集中在一处，
		// 每个 Pod 写出的文件集合也可预测。
		group := systemenv.WorkerGroup()
		if group == "" {
			group = "default"
		}
		dir := filepath.Join("storage", "log", "worker", "{date}", group)
		cfg.Log.Outputs = append(cfg.Log.Outputs, filepath.Join(dir, "info.log"))
		cfg.Log.ErrorOutputs = append(cfg.Log.ErrorOutputs, filepath.Join(dir, "error.log"))
		cfg.Log.SystemErrorOutputs = append(cfg.Log.SystemErrorOutputs, filepath.Join(dir, "system_error.log"))
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
