package gofy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/internal/pidfile"
	"github.com/bingcool/gofy/internal/process"
	"github.com/bingcool/gofy/internal/proclock"
	"github.com/bingcool/gofy/support"
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
		return a.daemonizeWorker(groups, pidPath, opts)
	}

	if err := a.beginStart(false); err != nil {
		return err
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	a.cancel = cancel

	logs, err := a.buildLoggers()
	if err != nil {
		return a.abort(err)
	}
	a.setLogger(logs)
	lg := logs.app

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

	// Fail before anything starts when a worker cannot run without db.default.
	// The check has to sit here rather than inside Run: a consumer that enables
	// the idempotency ledger and finds no pool returns an error from Run, and
	// the default RestartOnError policy would restart it forever while
	// readiness never closes — a process that looks alive and serves nothing.
	// At this point the same condition is still a startup error carrying a
	// reason, and the operator sees it in the terminal instead of a 503 loop.
	//
	// Only workers that declare the requirement are checked: a group built on
	// named instances (db.order) is a valid deployment without db.default, and
	// rejecting it here would undo the database-only generation.
	// 在启动任何东西之前，拦下"离不开 db.default 的 worker"。检查必须在这里而不是 Run
	// 里：开启幂等台账却找不到池的消费者会从 Run 返回错误，默认的 RestartOnError 策略于是
	// 无限重启它、就绪永不放行——一个看着活着、什么也不干的进程。在这个位置，同一个条件
	// 仍是带原因的启动错误，运维在终端就能看到，而不是面对 503 循环。
	//
	// 只检查声明了该要求的 worker：靠命名实例（db.order）构建的分组在没有 db.default 时
	// 是合法部署，在这里拒绝它会废掉"只装数据库"的那一代。
	if err := worker.RequireDefaultDB(groups, rt.DB != nil); err != nil {
		return a.abort(err)
	}

	// Worker-side IO assembly. A worker process never runs ModuleFactories, so
	// without this there is no published generation and every entry would have to
	// fake one out of Runtime.DB. The position carries two meanings: before the
	// groups (a first tick must already resolve a handle) and inside cleanup
	// (Stop runs after the drain and before the database closes).
	// worker 侧 IO 装配。worker 进程从不运行 ModuleFactories，没有这一步就没有已发布
	// 的代际，每个入口只能从 Runtime.DB 伪造一个。位置有两重意义：放在分组之前（首个
	// tick 就必须能解析到句柄），以及交给 cleanup（Stop 排在 drain 之后、关库之前）。
	if err := a.startWorkerModules(rt); err != nil {
		return a.abort(err)
	}

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
		release, err := a.claimWorkerPID(pidPath)
		if err != nil {
			return a.abort(err)
		}
		defer release()
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

	// runnerExit carries the runner alongside its result: an instance that
	// outlived the drain budget still needs its database, so the host has to
	// know which groups were abandoned before it tears anything down.
	// runnerExit 把 runner 和结果一起带回：超出 drain 预算的实例仍需要数据库，宿主
	// 必须在拆除资源之前知道哪些分组被放弃。
	type runnerExit struct {
		runner *worker.Runner
		err    error
	}
	errCh := make(chan runnerExit, len(runners))
	for _, runner := range runners {
		go func(r *worker.Runner) {
			errCh <- runnerExit{runner: r, err: r.Run(worker.WithRuntime(runCtx, rt))}
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
	var abandonedGroups []string
	remaining := len(runners)
	ready := false
	// A local alias so the shutdown case can be disarmed after it fires: a
	// closed channel is ready forever, and the loop ends on the runner errors
	// that follow.
	// 用局部别名，停机分支触发后可以就地停用它：已关闭的通道会一直就绪，而结束循环的是
	// 随后到达的 runner 错误。
	stopCh := a.stopCh
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
			// The Ready signal can already sit in the buffer when shutdown
			// starts. A process that is draining must not report ready: the
			// scheduler would keep sending work into the drain window.
			// 停机开始时 Ready 信号可能已经躺在缓冲里。正在 drain 的进程不能报就绪：
			// 否则调度器会继续把任务打进 drain 窗口。
			if runCtx.Err() != nil {
				continue
			}
			ready = true
			a.markReady()
			if health != nil {
				health.setReady()
			}
			lg.Info("worker group ready", startedFields...)
			// The parent that detached this process is waiting for exactly this
			// line before it prints success.
			// 脱离出本进程的父进程正等着这一行才打印成功。
			a.notifier.ready()
		case <-stopCh:
			// Programmatic shutdown. App.Shutdown closes stopCh and then waits
			// for a.done; the HTTP path listens to stopCh in run(), but a worker
			// process never reached that code, so stopping it this way waited
			// out the caller's deadline (or blocked forever on a background
			// context) while the workers kept consuming. Cancelling here gives
			// both entry points one stop path: run through the same drain, close
			// a.done, return.
			// 程序化停机。App.Shutdown 关闭 stopCh 后等 a.done；HTTP 路径在 run() 里
			// 监听 stopCh，而 worker 进程永远走不到那段代码，于是这样停机只会等满调用方
			// 的截止时间（用 Background 时永久阻塞），worker 还在继续消费。在这里取消，
			// 两条入口就共用同一个停机路径：走同一段排空、关闭 a.done、返回。
			cancelRunners(nil)
			stopCh = nil
		case exit := <-errCh:
			remaining--
			if exit.err != nil {
				runErr = errors.Join(runErr, exit.err)
				cancelRunners(exit.err)
			}
			if exit.runner.Abandoned() {
				abandonedGroups = append(abandonedGroups, exit.runner.Group().Name())
			}
		}
	}
	if len(abandonedGroups) > 0 {
		// The process is leaving, but handlers are still running: closing the
		// database under them turns a slow handler into a crash in a goroutine
		// nobody waits for. Keep the resources and let the OS reclaim them.
		// 进程要退出了，但 handler 还在跑：在它们脚下关数据库，会把"慢 handler"变成
		// 没人等待的 goroutine 崩溃。保留资源，交给 OS 回收。
		lg.Error("worker instances still running after the drain budget; keeping database and module resources open",
			zap.Int("abandoned_groups", len(abandonedGroups)),
			zap.Strings("groups", abandonedGroups),
		)
		a.abandonWorkers(abandonedGroups)
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
// detachProcessNotify is process.DetachWithNotify behind a variable: a test can
// observe the argv handed to the child and answer the startup report without
// spawning a process.
// detachProcessNotify 把 process.DetachWithNotify 放进变量：测试因此能在不真正拉起子进程
// 的情况下检查交给子进程的 argv，并回应启动报告。
var detachProcessNotify = process.DetachWithNotify

// claimWorkerPID makes this process the owner of one worker group.
//
// The lock file comes first, because it is the only atomic part: the kernel
// grants it to one open file description, so two starts can no longer both read
// an absent pid file, both see no live process and both write. The pid file
// stays as the address `worker stop` uses; the lock is what makes claiming it
// safe.
//
// The returned release function drops the lock. Callers keep it for the life of
// the process — on a crash the kernel releases it, which is why a stale .lock
// file from a killed process never blocks the next start. The file itself is
// never removed: deleting a lock file on release lets one process hold the
// unlinked inode while another locks a freshly created one, and both then
// believe they own the group.
// claimWorkerPID 让本进程成为一个 worker 分组的所有者。
//
// 先拿锁文件，因为它是唯一原子的部分：内核只把锁授予一个打开的文件描述，两个启动因此
// 不再可能都读到不存在的 pid 文件、都没看到活着的进程、然后都写入。pid 文件仍然是
// `worker stop` 使用的地址；锁保证"占住它"这件事是安全的。
//
// 返回的 release 释放锁。调用方在进程生命周期内一直持有——崩溃时内核会释放，因此被
// 杀掉的进程留下的 .lock 文件永远不会挡住下一次启动。文件本身从不删除：释放时删锁文件
// 会让一个进程锁住已删除的 inode，另一个进程锁住新建的 inode，两者都认为分组归自己。
func (a *App) claimWorkerPID(pidPath string) (func(), error) {
	lock, err := proclock.Acquire(pidPath + workerLockSuffix)
	if err != nil {
		if errors.Is(err, proclock.ErrHeld) {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, pidPath)
		}
		return nil, fmt.Errorf("gofy: %w", err)
	}
	release := func() { _ = lock.Close() }

	// The lock above already excludes another instance of this binary, so this
	// check only matters for a pid file left by an older build that never took
	// the lock. Only an IdentityMatch proves the recorded owner is still that
	// process: a mismatch is a recycled pid, and an unknown verdict cannot rule
	// reuse out, so both are overwritten instead of blocking the start.
	// 上面的锁已经排除了同一二进制的另一个实例，因此这次检查只对旧版本留下的、从未加锁的
	// pid 文件有意义。只有 IdentityMatch 能证明记录的进程仍占用该 pid：不匹配说明 pid
	// 被复用，未知则无法排除复用，两者都覆盖文件而不是挡住启动。
	if existing, readErr := pidfile.ReadRecord(pidPath); readErr == nil &&
		process.Alive(existing.PID) && existing.Verify() == pidfile.IdentityMatch {
		release()
		return nil, fmt.Errorf("%w: pid %d (%s)", ErrAlreadyRunning, existing.PID, pidPath)
	}
	if err := pidfile.WriteRecord(pidPath, pidfile.NewRecord(os.Getpid()), a.opts.pidPerm); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

const (
	// workerLockSuffix keeps the lock next to its pid file: same directory,
	// same group, one owner.
	// workerLockSuffix 把锁放在它的 pid 文件旁边：同一目录、同一分组、一个所有者。
	workerLockSuffix = ".lock"
)

func (a *App) daemonizeWorker(groups []*worker.Group, pidPath string, opts commandline.WorkerOptions) error {
	if runtime.GOOS == "windows" {
		// Name the operation, like `start --daemon` does. A bare ErrUnsupported
		// reads as "process: operation not supported on windows", which points at
		// the wrong package and gives the operator nothing to act on. Windows has
		// no ExtraFiles, so the startup channel that makes a detached worker
		// observable does not exist there either.
		// 像 `start --daemon` 那样点名操作。裸 ErrUnsupported 读起来是
		// "process: operation not supported on windows"，既指错了包，也没告诉运维该做什么。
		// Windows 没有 ExtraFiles，让脱离后的 worker 可观测的启动通道在那里同样不存在。
		return fmt.Errorf("gofy: worker --daemon is not supported on windows: %w", ErrUnsupported)
	}
	// Refuse before detaching. The child's stdout and stderr go to /dev/null, so a
	// duplicate instance would die silently while the parent reported success.
	// Only an IdentityMatch proves the owner is still that process; a recycled
	// pid and an unverifiable record are overwritten by the child.
	// 脱离之前先挡住重复实例：子进程的 stdout / stderr 都指向 /dev/null，重复实例
	// 会静默退出，而父进程已经报了成功。只有 IdentityMatch 能证明所有者仍是记录的
	// 进程；被复用的 pid 与无法验证的记录都由子进程覆盖。
	if pidPath != "" {
		if existing, err := pidfile.ReadRecord(pidPath); err == nil &&
			process.Alive(existing.PID) && existing.Verify() == pidfile.IdentityMatch {
			return fmt.Errorf("%w: pid %d (%s)", ErrAlreadyRunning, existing.PID, pidPath)
		}
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("gofy: resolve executable: %w", err)
	}
	// Never reuse os.Args: the parent's command may be `worker restart`, and a
	// detached restart would stop again and then wait for a confirmation no
	// terminal can answer. The child runs the start form only.
	// 绝不复用 os.Args：父进程的命令可能是 `worker restart`，脱离终端后再执行一次
	// restart 会重复 stop，然后等一个没有终端能回答的确认。子进程只执行启动形态。
	args := commandline.WorkerStartArgs(opts)
	// The child cannot be asked "did it work" through stdout — that goes to
	// /dev/null — so it gets a pipe and the parent waits for one line on it. Only
	// that line turns "a process was created" into "the group accepts work".
	// 子进程无法通过 stdout 回答"成功了吗"——那里是 /dev/null——因此它拿到一条管道，父进程在
	// 上面等一行。只有这一行能把"进程被创建了"变成"分组能接活"。
	pid, notify, err := detachProcessNotify(executable, args)
	if err != nil {
		return fmt.Errorf("gofy: daemonize worker: %w", err)
	}
	defer func() { _ = notify.Close() }()
	if err := awaitDaemonStartup(notify, pid, opts.StartupTimeout, "worker"); err != nil {
		return err
	}
	label := workerGroupLabel(groups)
	c := a.con()
	c.Success("worker 分组【%s】已在后台启动 (pid %d)", label, pid)
	// No binary name in the hint: how this process was launched is not something
	// the process can know — `go run .`, an absolute path and a bare name all
	// reach here — so naming demo-service would be a command that may not exist.
	// 提示里不带二进制名：本进程无从知道自己是怎么被启动的（`go run .`、绝对路径、
	// 裸命令名都会走到这里），写上 demo-service 反而可能是一条并不存在的命令。
	c.Note("使用 worker status --group=%s 查看状态，worker stop --group=%s 停止，worker restart --group=%s 重启",
		label, label, label)
	return nil
}

// stopWorker stops the worker processes selected by --group. An empty group (or
// the wildcard, in either spelling) stops every worker that left a pid file.
// stopWorker 停止 --group 选中的 worker 进程。分组为空（或通配符的任一写法）时停止
// 所有留下 pid 文件的 worker。
func (a *App) stopWorker(ctx context.Context, opts commandline.WorkerOptions) error {
	group := strings.TrimSpace(opts.Group)
	if group == "" {
		group = commandline.WorkerGroupAllAlias
	}
	return a.stop(ctx, commandline.StopOptions{
		Worker:  group,
		Force:   opts.Force,
		Timeout: opts.Timeout,
	})
}

// restartWorker stops the selected workers and starts them again in the same
// call, so `worker restart` cannot leave the service down if the second half is
// forgotten. Without --group it restarts the wildcard shape: one process hosting
// every registered group.
// restartWorker 在同一次调用里停止并重新启动选中的 worker，`worker restart` 因此不会
// 停在"只停了没起"的中间态。不带 --group 时按通配符重启：一个进程托管全部已注册分组。
func (a *App) restartWorker(ctx context.Context, opts commandline.WorkerOptions) error {
	group := strings.TrimSpace(opts.Group)
	if group == "" {
		group = commandline.WorkerGroupAllAlias
	}
	c := a.con()
	c.Info("正在重启 worker 分组【%s】", group)
	if err := a.stop(ctx, commandline.StopOptions{
		Worker:  group,
		Force:   opts.Force,
		Timeout: opts.Timeout,
	}); err != nil {
		if !isRestartIgnorableError(err) {
			return err
		}
		c.Note("未发现运行中的 worker，直接启动")
	}
	opts.Group = group
	return a.runWorker(ctx, opts)
}

// workerGroupLabel names the groups a daemonized process hosts, for messages.
// workerGroupLabel 为消息命名被托管的 daemon 分组。
func workerGroupLabel(groups []*worker.Group) string {
	if len(groups) == 1 {
		return groups[0].Name()
	}
	return commandline.WorkerAllPIDToken
}

// workerPIDGlob is the pattern matching every worker pid file derived from one
// configured pid path: the same -worker-<group> infix workerPIDPath inserts.
// workerPIDGlob 匹配由同一个 pid 路径派生出的全部 worker pid 文件，形态与
// workerPIDPath 插入的 -worker-<group> 一致。
func workerPIDGlob(base string) string {
	if strings.TrimSpace(base) == "" {
		return ""
	}
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext) + "-worker-*" + ext
}

// workerPIDFiles lists the worker pid files that exist for base, sorted so the
// output and the stop order are stable.
// workerPIDFiles 列出 base 对应的、确实存在的 worker pid 文件，并排序，使输出与停止
// 顺序稳定。
func workerPIDFiles(base string) ([]string, error) {
	pattern := workerPIDGlob(base)
	if pattern == "" {
		return nil, nil
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("gofy: list worker pid files: %w", err)
	}
	sort.Strings(matches)
	return matches, nil
}

// workerGroupFromPIDFile recovers the group name from a pid file path, so the
// operator sees which group is being stopped.
// workerGroupFromPIDFile 从 pid 文件路径反推分组名，运维因此能看到正在停止哪个分组。
func workerGroupFromPIDFile(base, file string) string {
	ext := filepath.Ext(base)
	prefix := filepath.Base(strings.TrimSuffix(base, ext)) + "-worker-"
	name := strings.TrimSuffix(filepath.Base(file), ext)
	return strings.TrimPrefix(name, prefix)
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

// startWorkerModules builds and starts the modules registered with
// WithWorkerModules. Factories run first as a group, so a construction error
// stops the process before any connection is opened; Start then runs in
// registration order and each started module joins a.started, which is what
// makes cleanup stop them in reverse order, after the groups drained and before
// the database they were handed is closed.
//
// Nothing here calls Register: a worker process has no Router. A module that
// only makes sense behind HTTP belongs in WithModuleFactories, where the HTTP
// path builds it.
// startWorkerModules 构造并启动 WithWorkerModules 注册的模块。工厂先整体执行，
// 因此构造错误在打开任何连接之前就让进程停下；随后按注册顺序 Start，每个启动成功的
// 模块记入 a.started——cleanup 据此逆序停止它们，位置在分组 drain 之后、它们拿到的
// 数据库被关闭之前。
//
// 这里不调用 Register：worker 进程没有 Router。只在 HTTP 之后才有意义的模块属于
// WithModuleFactories，由 HTTP 路径构建。
func (a *App) startWorkerModules(rt *core.Runtime) error {
	if len(a.opts.workerModules) == 0 {
		return nil
	}
	mods := make([]support.Module, 0, len(a.opts.workerModules))
	seen := make(map[string]struct{}, len(a.opts.workerModules))
	for _, factory := range a.opts.workerModules {
		m, err := factory(rt)
		if err != nil {
			return fmt.Errorf("gofy: build worker module: %w", err)
		}
		if m == nil {
			return errors.New("gofy: worker module factory returned nil")
		}
		if _, dup := seen[m.Name()]; dup {
			return fmt.Errorf("gofy: duplicate worker module %q", m.Name())
		}
		seen[m.Name()] = struct{}{}
		mods = append(mods, m)
	}
	for _, m := range mods {
		starter, ok := m.(support.Starter)
		if !ok {
			continue
		}
		if err := startModule(starter, rt.Context()); err != nil {
			return fmt.Errorf("gofy: worker module %q start: %w", m.Name(), err)
		}
		a.started = append(a.started, m)
	}
	return nil
}
