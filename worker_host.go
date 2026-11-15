package gofy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/internal/pidfile"
	"github.com/bingcool/gofy/internal/process"
	"github.com/bingcool/gofy/support/worker"
	"go.uber.org/zap"
)

// runWorker hosts the worker groups named by --group. It builds the same logger,
// database, telemetry and Runtime the HTTP path uses, but binds no routes, opens
// no listener and registers nothing with Nacos, so the process is a pure consumer.
// runWorker 托管 `--group` 指定的 worker 分组。它与 HTTP 路径共用 logger、数据库、
// 遥测与 Runtime，但不注册路由、不监听端口、不向 Nacos 注册，因此进程是纯消费者。
func (a *App) runWorker(parent context.Context, opts commandline.WorkerOptions) error {
	// Resolve the groups before anything is built: a typo in --group should not
	// open a database pool, start telemetry or write a log line first.
	// 先解析分组再构建任何资源：--group 拼错时不应该先去连数据库、启动遥测、写日志。
	groups, err := a.selectWorkerGroups(opts.Group)
	if err != nil {
		return err
	}
	if len(groups) > 1 && len(opts.Names) > 0 {
		// Each name belongs to one group, so filtering across groups would either
		// silently skip groups or need a per-group name list. Run the group that
		// owns the worker instead.
		// 每个名字只属于一个分组，跨组过滤要么静默跳过某些分组，要么得给每组单独
		// 列名字。直接运行拥有该 worker 的那个分组即可。
		return fmt.Errorf("%w: --name cannot be combined with --group=%s; name the group that owns the worker",
			ErrUsage, commandline.WorkerGroupAll)
	}
	if len(groups) == 1 {
		if groups[0], err = groups[0].Subset(opts.Names); err != nil {
			return err
		}
	}

	// Resolve the pid file before anything is built. The daemon path needs it to
	// refuse a duplicate instance while the error can still reach the terminal;
	// the foreground path reuses the same value below.
	//
	// The worker process keeps its own pid file: sharing the HTTP one would let
	// `stop` kill the API server and make a second group look like a duplicate
	// instance. Running every group at once still owns a single pid file, named
	// after the wildcard rather than after a group that is not the whole process.
	// 在构建任何资源之前先解析 pid 文件：daemon 路径需要它在错误还能打到终端时挡住重复
	// 实例，前台路径随后复用同一个值。
	//
	// worker 进程使用独立 pid 文件：共用 HTTP 的那个会让 `stop` 误杀 API 服务，
	// 也会让第二个分组被误判为重复实例。全部分组同跑时仍然只有一个 pid 文件，
	// 名字取通配符而不是某个并不代表整个进程的分组名。
	pidPath, err := a.resolvePIDPath()
	if err != nil {
		return err
	}
	pidPath = workerPIDPath(pidPath, workerPIDGroup(groups))
	if opts.Daemon {
		return a.daemonizeWorker(groups, pidPath)
	}

	if err := a.beginStart(false); err != nil {
		return err
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	a.cancel = cancel

	lg, system, access, own, err := a.buildLoggers()
	if err != nil {
		return a.abort(err)
	}
	a.setLogger(lg, system, access, own)

	telemetry, err := a.buildTelemetry(ctx, lg)
	if err != nil {
		return a.abort(err)
	}
	a.telemetry = telemetry

	db, ownDB, err := a.buildDB(ctx, lg)
	if err != nil {
		return a.abort(err)
	}
	if err := a.instrumentDB(db, ownDB); err != nil {
		return a.abort(err)
	}
	a.db, a.ownDB = db, ownDB

	rt := core.NewRuntime(core.RuntimeConfig{
		Name:           a.opts.name,
		Env:            a.opts.env,
		Logger:         lg,
		DB:             db,
		Version:        a.version,
		Ctx:            ctx,
		Fail:           func(err error) { cancel(err) },
		TracerProvider: telemetry.TracerProvider(),
		Propagator:     telemetry.Propagator(),
		Tracing:        telemetry.Enabled(),
	})
	a.rt = rt

	runners := make([]*worker.Runner, len(groups))
	for i, group := range groups {
		runners[i], err = worker.NewRunner(group, worker.RunConfig{
			Policy:       a.opts.workerPolicy,
			Concurrency:  a.workerConcurrency(group, opts.Concurrency),
			DrainTimeout: a.opts.workerDrain,
			Logger:       lg,
		})
		if err != nil {
			return a.abort(err)
		}
	}

	if pidPath != "" {
		if existing, readErr := pidfile.Read(pidPath); readErr == nil && process.Alive(existing) {
			return a.abort(fmt.Errorf("%w: pid %d (%s)", ErrAlreadyRunning, existing, pidPath))
		}
		if err := pidfile.Write(pidPath, os.Getpid(), a.opts.pidPerm); err != nil {
			return a.abort(err)
		}
		a.pidPath = pidPath
		a.startPIDRefresh(lg)
	}

	names := make([]string, len(groups))
	workers := 0
	for i, group := range groups {
		names[i] = group.Name()
		workers += len(group.Workers())
	}
	startedFields := []zap.Field{
		zap.String("name", a.opts.name),
		zap.String("env", a.opts.env),
		zap.Int("workers", workers),
		zap.Duration("drain_timeout", a.opts.workerDrain),
		zap.Bool("telemetry", telemetry.Enabled()),
	}
	if len(groups) == 1 {
		startedFields = append(startedFields, zap.String("group", groups[0].Name()))
		lg.Info("worker group started", startedFields...)
	} else {
		startedFields = append(startedFields, zap.Strings("groups", names))
		lg.Info("worker groups started", startedFields...)
	}

	// The probe comes up before the group does so a scheduler that restarts on
	// a failed readiness check sees 503 instead of a connection refused, which
	// is the difference between "starting" and "wrong port".
	// 探针先于分组启动，调度器因就绪检查失败而重启时看到的是 503 而不是连接被拒，
	// 这正是"还在启动"与"端口配错了"的区别。
	var health *workerHealth
	if a.opts.workerHealth != "" {
		health = newWorkerHealth(a.opts.workerHealth)
		if err := health.start(); err != nil {
			return a.abort(fmt.Errorf("gofy: worker health endpoint: %w", err))
		}
		defer health.stop()
	}

	// Workers reach the logger, database and tracer through the runtime, the
	// same way a script receives it as an argument.
	// worker 通过 Runtime 访问 logger、数据库与 tracer，方式与脚本接收 Runtime
	// 参数一致。
	runCtx, cancelRunners := context.WithCancelCause(ctx)
	defer cancelRunners(nil)
	// The probe flips to 503 the moment the shutdown begins, not after the last
	// runner returned: the whole drain window accepts no new work, and a
	// scheduler that keeps sending traffic until the process exits would turn a
	// graceful stop into a burst of failed requests.
	// 停机一开始探针就转 503，而不是等最后一个 runner 返回：整个 drain 窗口都不接
	// 新任务，否则调度器会一直往正在退出的进程打流量，把优雅停机变成一批失败请求。
	if health != nil {
		go func() {
			<-runCtx.Done()
			health.setStopping()
		}()
	}

	errCh := make(chan error, len(runners))
	for _, runner := range runners {
		go func(r *worker.Runner) {
			errCh <- r.Run(worker.WithRuntime(runCtx, rt))
		}(runner)
	}

	// Ready is a claim about work being accepted, not about goroutines having
	// been started: a consumer connects and subscribes inside Run, so the probe
	// stays 503 until every instance of every group reported readiness.
	// 就绪说的是"能接活"，不是"goroutine 已启动"：消费者在 Run 内部建连并订阅，
	// 因此探针会一直 503，直到每个分组的每个实例都上报就绪。
	readyAll := make(chan bool, 1)
	go func() {
		for _, runner := range runners {
			select {
			case <-runner.Ready():
			case <-runCtx.Done():
				readyAll <- false
				return
			}
		}
		readyAll <- true
	}()

	var runErr error
	remaining := len(runners)
	ready := false
	// One group failing takes the process down: a process that keeps half of
	// its groups alive would report ready while silently skipping work, and the
	// wildcard exists for local runs where "all or nothing" is the useful
	// answer.
	// 一个分组失败就带走整个进程：只留一半分组活着的进程会一边报就绪一边漏活；
	// 通配符本来就是给本地运行用的，"全有或全无"才是想要的答案。
	for remaining > 0 {
		select {
		case allReady := <-readyAll:
			// A nil channel blocks forever, which is what "the answer already
			// arrived" means for the rest of this loop.
			// nil 通道会永久阻塞，这正是"答案已经拿到"在本次循环剩余部分里的含义。
			readyAll = nil
			if !allReady || ready {
				continue
			}
			ready = true
			a.markReady()
			if health != nil {
				health.setReady()
			}
			lg.Info("worker group ready", startedFields...)
		case err := <-errCh:
			remaining--
			if err != nil {
				runErr = errors.Join(runErr, err)
				cancelRunners(err)
			}
		}
	}
	if cerr := a.cleanup(); cerr != nil {
		runErr = errors.Join(runErr, cerr)
	}
	if runErr == nil {
		if len(groups) == 1 {
			lg.Info("worker group stopped", zap.String("group", groups[0].Name()))
		} else {
			lg.Info("worker groups stopped", zap.Strings("groups", names))
		}
	} else if len(groups) == 1 {
		lg.Error("worker group stopped with error",
			zap.String("group", groups[0].Name()), zap.Error(runErr))
	} else {
		lg.Error("worker groups stopped with error",
			zap.Strings("groups", names), zap.Error(runErr))
	}
	return runErr
}

// selectWorkerGroups resolves --group to the groups this process should run.
// A single name returns that group; the wildcard — in either spelling, since one
// of them exists only because shells eat * — returns every registered group,
// which is a local-development and smoke-test mode rather than a deployment
// shape: pid file, probe and `stop` stay process-wide.
// selectWorkerGroups 把 --group 解析成进程要运行的分组：单个名字返回该分组，通配符返回
// 全部已注册分组（两种写法等价，别名存在的唯一原因是 shell 会吃掉 *）。后者是本地开发
// 与冒烟测试的用法，不是部署形态——pid 文件、探针与 `stop` 的粒度仍然是进程。
func (a *App) selectWorkerGroups(name string) ([]*worker.Group, error) {
	if commandline.IsAllWorkerGroup(name) {
		if len(a.opts.workerGroups) == 0 {
			return nil, fmt.Errorf("%w: no worker groups registered", ErrWorkerGroupNotFound)
		}
		return append([]*worker.Group(nil), a.opts.workerGroups...), nil
	}
	group, err := a.workerGroup(name)
	if err != nil {
		return nil, err
	}
	return []*worker.Group{group}, nil
}

// workerPIDGroup names the pid file for the resolved groups: the group name when
// one runs, and the wildcard token when the process hosts every group.
// workerPIDGroup 为解析出的分组决定 pid 文件名：只跑一个分组时用组名，进程托管全部分组
// 时用通配符对应的名字。
func workerPIDGroup(groups []*worker.Group) string {
	if len(groups) == 1 {
		return groups[0].Name()
	}
	return commandline.WorkerAllPIDToken
}

// daemonizeWorker re-executes this process in its own session and exits the
// parent, the same way `start --daemon` does. The child gets --daemon=false so it
// runs in the foreground of that session instead of detaching again.
// daemonizeWorker 在一个独立会话里重新执行本进程并让父进程退出，做法与
// `start --daemon` 相同。子进程收到 --daemon=false，因此它在该会话里前台运行，不会
// 再次脱离。
func (a *App) daemonizeWorker(groups []*worker.Group, pidPath string) error {
	if runtime.GOOS == "windows" {
		return ErrUnsupported
	}
	// Refuse before detaching. The child's stdout and stderr go to /dev/null, so a
	// duplicate instance would die silently while the parent reported success.
	// 脱离之前先挡住重复实例：子进程的 stdout / stderr 都指向 /dev/null，重复实例
	// 会静默退出，而父进程已经报了成功。
	if pidPath != "" {
		if existing, err := pidfile.Read(pidPath); err == nil && process.Alive(existing) {
			return fmt.Errorf("%w: pid %d (%s)", ErrAlreadyRunning, existing, pidPath)
		}
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("gofy: resolve executable: %w", err)
	}
	args := append(append([]string{}, os.Args[1:]...), "--daemon=false")
	pid, err := process.Detach(executable, args)
	if err != nil {
		return fmt.Errorf("gofy: daemonize worker: %w", err)
	}
	label := workerGroupLabel(groups)
	c := a.con()
	c.Success("worker 分组【%s】已在后台启动 (pid %d)", label, pid)
	// No binary name in the hint: how this process was launched is not something
	// the process can know — `go run .`, an absolute path and a bare name all
	// reach here — so naming demo-service would be a command that may not exist.
	// 提示里不带二进制名：本进程无从知道自己是怎么被启动的（`go run .`、绝对路径、
	// 裸命令名都会走到这里），写上 demo-service 反而可能是一条并不存在的命令。
	c.Note("使用 stop --worker=%s 停止", label)
	return nil
}

// workerGroupLabel names the groups a daemonized process hosts, for messages.
// workerGroupLabel 为消息命名被托管的 daemon 分组。
func workerGroupLabel(groups []*worker.Group) string {
	if len(groups) == 1 {
		return groups[0].Name()
	}
	return commandline.WorkerAllPIDToken
}

// workerGroup looks up a registered group by name.
// workerGroup 按名字查找已注册的分组。
func (a *App) workerGroup(name string) (*worker.Group, error) {
	name = strings.TrimSpace(name)
	for _, g := range a.opts.workerGroups {
		if g.Name() == name {
			return g, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrWorkerGroupNotFound, name)
}

// workerDescriptors describes the registered groups for `worker --list`.
// workerDescriptors 为 `worker --list` 描述已注册的分组。
func (a *App) workerDescriptors() []commandline.WorkerGroup {
	out := make([]commandline.WorkerGroup, 0, len(a.opts.workerGroups))
	for _, g := range a.opts.workerGroups {
		workers := g.Workers()
		infos := make([]commandline.WorkerInfo, 0, len(workers))
		for _, w := range workers {
			infos = append(infos, commandline.WorkerInfo{
				Name:        w.Name(),
				Concurrency: a.workerConcurrencyFor(g.Name(), w),
			})
		}
		out = append(out, commandline.WorkerGroup{Name: g.Name(), Workers: infos})
	}
	return out
}

// workerConcurrencyFor resolves one worker's instance count: an explicit option
// wins, then the count the worker declares, then one. `worker --list` uses it,
// so the table shows what will actually start.
// workerConcurrencyFor 解析单个 worker 的实例数：显式选项优先，其次 worker 自己声明
// 的计数，最后是 1。`worker --list` 用它，因此表里显示的就是实际会启动的数量。
func (a *App) workerConcurrencyFor(group string, w worker.Worker) int {
	if n, ok := a.opts.workerConcurrency[group+"/"+w.Name()]; ok && n > 0 {
		return n
	}
	if c, ok := w.(worker.Concurrent); ok && c.Concurrency() > 0 {
		return int(c.Concurrency())
	}
	return 1
}

// workerConcurrency builds the runner's override map. Only explicit overrides
// are included: a worker's own count has to survive when neither the option nor
// the --concurrency flag mentions it.
// workerConcurrency 构造 Runner 的覆盖映射。只有显式覆盖才会进表：选项与
// --concurrency 都没提到某个 worker 时，它自己声明的计数必须保留下来。
func (a *App) workerConcurrency(group *worker.Group, override int) map[string]int {
	workers := group.Workers()
	out := make(map[string]int, len(workers))
	for _, w := range workers {
		if override > 0 {
			out[w.Name()] = override
			continue
		}
		if n, ok := a.opts.workerConcurrency[group.Name()+"/"+w.Name()]; ok && n > 0 {
			out[w.Name()] = n
		}
	}
	return out
}

// workerPIDPath inserts -worker-<group> before the extension so one host can
// run several groups and the HTTP server without pid collisions.
// workerPIDPath 在扩展名前插入 -worker-<group>，使同一台主机上的多个分组与
// HTTP 服务不会争用同一个 pid 文件。
func workerPIDPath(path, group string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + "-worker-" + group + ext
}
