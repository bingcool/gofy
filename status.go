package gofy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/internal/pidfile"
	"github.com/bingcool/gofy/internal/process"
)

// Process states reported by `status`. They are strings, not numbers, because
// the consumer of the JSON form is a shell script: `[ "$(status --json | jq -r
// .processes[0].status)" = running ]` reads better than decoding an enum.
// status 报告的状态用字符串而不是数字：JSON 的消费方是 shell 脚本，
// `[ "$(... | jq -r .status)" = running ]` 比解码一个枚举好读。
const (
	statusRunning    = "running"
	statusUnverified = "unverified"
	statusStopped    = "stopped"
	statusStale      = "stale"
	statusMissing    = "missing"
)

// statusHealthTimeout bounds the /healthz probe. A status command that hangs on
// an unresponsive process is worse than one that says "unreachable".
// statusHealthTimeout 限制 /healthz 探测。卡死的 status 命令比一句 "unreachable" 更糟。
const statusHealthTimeout = time.Second

// statusHTTPClient probes the local health endpoint. It never uses the
// environment proxy: a localhost health check routed through HTTP_PROXY reports
// the proxy's answer, not the process's.
// statusHTTPClient 探测本机健康端点。它不走环境代理：经 HTTP_PROXY 转发的本机健康检查
// 拿到的是代理的答复，不是进程的。
var statusHTTPClient = &http.Client{
	Timeout:   statusHealthTimeout,
	Transport: &http.Transport{Proxy: nil},
}

// processStatus is one row of `status`: the HTTP instance, or one worker
// process. Uptime comes from the pid file's claim time, so it survives a
// restart of the status command and needs no platform-specific process
// inspection; a legacy pid file without a claim time reports no uptime rather
// than a wrong one.
// processStatus 是 `status` 的一行：HTTP 实例，或一个 worker 进程。运行时长取自 pid
// 文件的登记时刻，因此与 status 命令本身是否重启无关，也不需要平台相关的进程探测；
// 旧格式的 pid 文件没有登记时刻，此时不报运行时长，而不是报一个错的。
type processStatus struct {
	// Group is set for worker processes only.
	// Group 只对 worker 进程设置。
	Group string `json:"group,omitempty"`
	// PID is the pid recorded in the file; zero when there is no usable file.
	// PID 是文件里记录的进程号；没有可用文件时为零。
	PID int `json:"pid"`
	// Status is running, unverified, stopped, stale or missing. "unverified"
	// means the process is alive but the pid file cannot prove it is still the
	// same process — a script must not treat that as a healthy "running".
	// Status 取 running、unverified、stopped、stale 或 missing。unverified 表示进程活着，
	// 但 pid 文件无法证明它仍是原来那个进程，脚本不能把它当成健康的 running。
	Status string `json:"status"`
	// Verified reports whether the pid still belongs to the process that wrote
	// the file. False with Status=running means "alive, but unproven".
	// Verified 表示该 pid 是否仍属于写下文件的进程。Status=running 时为 false 表示
	// "活着，但无法证明是它"。
	Verified bool `json:"identity_verified"`
	// UptimeSeconds is how long ago the pid file was claimed.
	// UptimeSeconds 是 pid 文件登记至今的秒数。
	UptimeSeconds int64 `json:"uptime_seconds,omitempty"`
	// Health is ok, a status code, unreachable or disabled.
	// Health 取 ok、状态码、unreachable 或 disabled。
	Health string `json:"health,omitempty"`
	// PIDFile is the absolute path that was read.
	// PIDFile 是读取的绝对路径。
	PIDFile string `json:"pid_file"`
}

// serviceStatus is the whole document: one process for the HTTP form, one per
// worker process for the worker form. Keeping one shape for both means a script
// that watches workers and a script that watches the server read the same path.
// serviceStatus 是整份文档：HTTP 形态下一个进程，worker 形态下每个 worker 进程一条。
// 两种形态共用一个结构，盯 worker 的脚本与盯服务的脚本因此读同一个路径。
type serviceStatus struct {
	Name      string          `json:"name"`
	Role      string          `json:"role"`
	Addr      string          `json:"addr,omitempty"`
	Version   string          `json:"version"`
	Processes []processStatus `json:"processes"`
}

// status reports the HTTP instance recorded in the pid file. It is read-only:
// a stale file is described, never removed, because deleting it here would race
// with a process that is starting.
// status 报告 pid 文件记录的 HTTP 实例。它只读：失效的 pid 文件会被描述而不会被删除，
// 在这里删除会与正在启动的进程抢同一个文件。
func (a *App) status(ctx context.Context, opts commandline.StatusOptions) error {
	path := strings.TrimSpace(opts.PIDFile)
	if path == "" {
		path = a.opts.pidFile
	}
	if path == "" {
		return ErrPIDFileDisabled
	}
	file, err := a.pidFilePath(path)
	if err != nil {
		return err
	}

	entry := describePIDFile(ctx, file, a.httpHealthAddr())
	doc := serviceStatus{
		Name:      a.opts.name,
		Role:      "http",
		Addr:      a.httpAddr(),
		Version:   a.version.Version,
		Processes: []processStatus{entry},
	}
	if opts.JSON {
		if err := a.writeStatusJSON(doc); err != nil {
			return err
		}
	} else if entry.Status == statusRunning || entry.Status == statusUnverified {
		// Show the table for an unverified pid too; the non-nil return below
		// still makes the command fail, so the operator sees both the details
		// and the reason instead of only an error line.
		// 身份未校验时也把表格打出来；下面仍返回非 nil 让命令失败，运维因此能同时看到
		// 详情和原因，而不是只有一行错误。
		c := a.con()
		c.Success("应用【%s】运行中", a.opts.name)
		c.Table([][]string{
			{"服务应用", a.opts.name},
			{"运行状态", displayStatus(entry)},
			{"进程 PID", strconv.Itoa(entry.PID)},
			{"已运行", humanUptime(entry)},
			{"监听地址", "http://" + a.httpAddr()},
			{"健康检查", orDash(entry.Health)},
			{"PID 文件", a.displayPIDFile(file)},
			{"版本", a.version.Version},
		})
	}
	// The failure is returned, not printed here: a command that both prints an
	// error and returns one produces two lines for one problem. Wrapping the
	// reason keeps the single line that does get printed specific.
	// 失败只返回、不在这里打印：既打印又返回会让一个问题出现两行。把原因包进错误里，
	// 最终那一行才说得具体。
	if entry.Status != statusRunning {
		return fmt.Errorf("%w: %s: %s", ErrNotRunning, statusReason(entry), file)
	}
	return nil
}

// workerStatus reports every worker process that left a pid file, or the one
// selected group. A group that was started separately has its own file, so
// reading only the configured path would report "not running" for a host full
// of workers.
// workerStatus 报告所有留下 pid 文件的 worker 进程，或选中的那一个分组。分组分别启动时
// 各写一个文件，只读配置里的那个路径会对一整个主机的 worker 报"未运行"。
func (a *App) workerStatus(ctx context.Context, opts commandline.WorkerOptions) error {
	if a.opts.pidFile == "" {
		return ErrPIDFileDisabled
	}
	base, err := a.pidFilePath(a.opts.pidFile)
	if err != nil {
		return err
	}

	group := strings.TrimSpace(opts.Group)
	// A single group is interpolated into a pid file name below.
	// 单个分组会进入下面的 pid 文件名。
	if err := validateWorkerGroupSelector(group); err != nil {
		return err
	}
	var files []string
	if group == "" || commandline.IsAllWorkerGroup(group) {
		files, err = workerPIDFiles(base)
		if err != nil {
			return err
		}
	} else {
		// The run form writes `<pid file>-worker-<group>`, so asking about one
		// group has to resolve the same path instead of every file on the host.
		// 运行形态写的是 `<pid 文件>-worker-<分组>`，因此问某一个分组时要解析同一个
		// 路径，而不是主机上的所有文件。
		files = []string{workerPIDPath(base, group)}
	}

	entries := make([]processStatus, 0, len(files))
	running := 0
	for _, file := range files {
		entry := describePIDFile(ctx, file, a.opts.workerHealth)
		entry.Group = workerGroupFromPIDFile(base, file)
		if entry.Status == statusRunning {
			running++
		}
		entries = append(entries, entry)
	}

	doc := serviceStatus{
		Name:      a.opts.name,
		Role:      "worker",
		Version:   a.version.Version,
		Processes: entries,
	}
	if opts.JSON {
		if err := a.writeStatusJSON(doc); err != nil {
			return err
		}
	} else if len(entries) > 0 {
		// The HEALTH column appears only when this deployment configures a
		// worker health address: a column that can never say anything is noise,
		// and "--" repeated down the table reads like a failure.
		// 只有这套部署配了 worker 健康地址时才出现 HEALTH 列：一列永远说不出东西是噪音，
		// 整列重复的 "--" 会被读成故障。
		withHealth := strings.TrimSpace(a.opts.workerHealth) != ""
		header := []string{"GROUP", "PID", "STATUS", "UPTIME"}
		if withHealth {
			header = append(header, "HEALTH")
		}
		rows := make([][]string, 0, len(entries)+1)
		rows = append(rows, append(header, "PID FILE"))
		for _, entry := range entries {
			row := []string{
				orDash(entry.Group),
				strconv.Itoa(entry.PID),
				displayStatus(entry),
				humanUptime(entry),
			}
			if withHealth {
				row = append(row, orDash(entry.Health))
			}
			rows = append(rows, append(row, a.displayPIDFile(entry.PIDFile)))
		}
		a.con().Table(rows)
	}
	if running == 0 {
		// Naming the group when one was selected keeps the error pointed: "no
		// worker is running" would be true but useless if the operator asked
		// about group3 and the other groups are fine.
		// 选中单个分组时在错误里点名，问题才指向具体位置：运维问的是 group3，而别的分组
		// 都正常时，"no worker is running" 正确但没用。
		if group != "" && !commandline.IsAllWorkerGroup(group) && len(entries) == 1 {
			return fmt.Errorf("%w: worker %q: %s: %s", ErrNotRunning, group, statusReason(entries[0]), a.displayPIDFile(files[0]))
		}
		if len(entries) == 0 {
			return fmt.Errorf("%w: no worker pid file matches %s", ErrNotRunning, a.displayPIDFile(workerPIDGlob(base)))
		}
		return fmt.Errorf("%w: no worker process is running in %s", ErrNotRunning, a.displayPIDFile(workerPIDGlob(base)))
	}
	return nil
}

// describePIDFile turns one pid file into a row. It answers three questions in
// order and stops at the first that settles it: is there a file, is the pid
// alive, is the pid still ours. Only a running, verified process gets a health
// probe — probing a recycled pid would report a stranger's answer.
// describePIDFile 把一个 pid 文件变成一行。它按顺序问三个问题并在第一个能定论的答案处
// 停下：文件在不在、pid 还活着吗、pid 还是不是我们的。只有活着且已确认的进程才做健康
// 探测——探测一个被回收的 pid 得到的是陌生进程的答复。
func describePIDFile(ctx context.Context, file, healthAddr string) processStatus {
	entry := processStatus{PIDFile: file, Status: statusMissing}
	record, err := pidfile.ReadRecord(file)
	if err != nil {
		return entry
	}
	entry.PID = record.PID
	if !process.Alive(record.PID) {
		entry.Status = statusStopped
		return entry
	}
	switch record.Verify() {
	case pidfile.IdentityMismatch:
		entry.Status = statusStale
		return entry
	case pidfile.IdentityUnknown:
		// The pid is alive but its identity cannot be proven: a legacy file,
		// an unsupported platform or an uninspectable process. Reporting
		// "running" here would let a monitoring script accept a recycled pid.
		// pid 活着但身份无法证明：旧格式文件、平台不支持或进程不可读。此时报 running
		// 会让监控脚本把一个被复用的 pid 当成正常进程。
		entry.Status = statusUnverified
		return entry
	case pidfile.IdentityMatch:
		entry.Verified = true
	}
	entry.Status = statusRunning
	if claimed, err := time.Parse(time.RFC3339Nano, record.ClaimedAt); err == nil && !claimed.IsZero() {
		up := time.Since(claimed)
		if up < 0 {
			up = 0
		}
		entry.UptimeSeconds = int64(up / time.Second)
	}
	entry.Health = probeHealth(ctx, healthAddr)
	return entry
}

// probeHealth asks the local /healthz. An empty address means the endpoint is
// not configured, which is different from "it did not answer": the first is a
// configuration fact, the second is a symptom.
// probeHealth 询问本机 /healthz。地址为空表示没配这个端点，与"没有应答"不同：前者是
// 配置事实，后者是症状。
func probeHealth(ctx context.Context, addr string) string {
	target := healthURL(addr)
	if target == "" {
		return "disabled"
	}
	ctx, cancel := context.WithTimeout(ctx, statusHealthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "unreachable"
	}
	resp, err := statusHTTPClient.Do(req)
	if err != nil {
		return "unreachable"
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return "ok"
	}
	return fmt.Sprintf("not ready (%d)", resp.StatusCode)
}

// healthURL builds the probe URL. A listener bound to 0.0.0.0 or :: is not
// dialable as such, so the wildcard is replaced with the loopback address: the
// status command runs on the same host by construction.
// healthURL 构造探测地址。监听在 0.0.0.0 或 :: 上的端口不能照原样拨号，因此把通配地址
// 换成本机回环：status 命令按定义就跑在同一台主机上。
func healthURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	parsed, err := url.Parse(addr)
	if err != nil {
		return ""
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
	}
	host := parsed.Hostname()
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}

// httpAddr is the configured listen address, or "" when the port is unknown.
// It reports what the process was configured with, not what it is bound to:
// status runs in another process and cannot read the listener.
// httpAddr 是配置里的监听地址；端口未知时为空。它报告进程的配置值而不是实际绑定值：
// status 跑在另一个进程里，读不到那个 listener。
func (a *App) httpAddr() string {
	if a.opts.http.Port <= 0 {
		return ""
	}
	host := a.opts.http.Host
	if host == "" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(a.opts.http.Port))
}

// httpHealthAddr is the address to probe, or "" when /healthz is disabled. The
// endpoints are off by default, so "disabled" has to be the normal answer.
// httpHealthAddr 是要探测的地址；/healthz 未启用时为空。端点默认关闭，因此
// "disabled" 必须是常态答案。
func (a *App) httpHealthAddr() string {
	if !a.opts.health {
		return ""
	}
	return a.httpAddr()
}

func (a *App) pidFilePath(path string) (string, error) {
	workDir, err := a.resolveWorkDir()
	if err != nil {
		return "", err
	}
	return pidfile.Resolve(path, workDir), nil
}

// displayPIDFile shortens an absolute path to the form an operator typed when
// the file lives under the working directory. The JSON keeps the absolute path:
// a script should not have to guess the process's cwd.
// displayPIDFile 在文件位于工作目录之下时把绝对路径缩成运维敲的那个形态。JSON 保留
// 绝对路径：脚本不应该去猜进程的工作目录。
func (a *App) displayPIDFile(file string) string {
	workDir, err := a.resolveWorkDir()
	if err != nil {
		return file
	}
	rel, err := filepath.Rel(workDir, file)
	if err != nil || strings.HasPrefix(rel, "..") {
		return file
	}
	return rel
}

func (a *App) writeStatusJSON(doc serviceStatus) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("gofy: encode status: %w", err)
	}
	_, err = fmt.Fprintln(a.con().Out, string(data))
	return err
}

// statusReason explains a non-running state for the one-line error. The
// distinction matters on call: "stopped" is an expected state, "stale" means
// someone else owns the pid now, and "missing" means the file was never written
// (or was removed), which points at a different problem.
// statusReason 为那一行错误说明未运行的原因。区分是有意义的：pid 已退出是预期状态，
// pid 被别的进程占用说明文件已失效，而找不到文件说明它从未写入（或被删掉），指向的是
// 另一个问题。
func statusReason(entry processStatus) string {
	switch entry.Status {
	case statusMissing:
		return "找不到 pid 文件"
	case statusStopped:
		return fmt.Sprintf("pid %d 已退出", entry.PID)
	case statusStale:
		return fmt.Sprintf("pid %d 已被其他进程占用", entry.PID)
	case statusUnverified:
		return fmt.Sprintf("pid %d 的身份无法校验，需要 --force 才能停止", entry.PID)
	}
	return "未运行"
}

// displayStatus adds the one qualifier the four states cannot carry: a running
// process whose pid could not be verified. Saying just "running" would claim
// more than was checked.
// displayStatus 补上四个状态装不下的限定：活着但 pid 无法确认的进程。只写 "running"
// 会声称比实际校验过的更多。
func displayStatus(entry processStatus) string {
	if entry.Status == statusRunning && !entry.Verified {
		return "running (unverified)"
	}
	return entry.Status
}

func humanUptime(entry processStatus) string {
	if entry.Status != statusRunning || entry.UptimeSeconds <= 0 {
		return "--"
	}
	return (time.Duration(entry.UptimeSeconds) * time.Second).String()
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "--"
	}
	return s
}
