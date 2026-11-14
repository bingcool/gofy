package gofy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/internal/commandline"
	"github.com/bingcool/gofy/internal/pidfile"
	"github.com/bingcool/gofy/internal/process"
	"github.com/bingcool/gofy/support/worker"
	"go.uber.org/zap"
)

// runWorker hosts one worker group. It builds the same logger, database,
// telemetry and Runtime the HTTP path uses, but binds no routes, opens no
// listener and registers nothing with Nacos, so the process is a pure consumer.
// runWorker 托管一个 worker 分组。它与 HTTP 路径共用 logger、数据库、遥测与
// Runtime，但不注册路由、不监听端口、不向 Nacos 注册，因此进程是纯消费者。
func (a *App) runWorker(parent context.Context, opts commandline.WorkerOptions) error {
	// Resolve the group before anything is built: a typo in --group should not
	// open a database pool, start telemetry or write a log line first.
	// 先解析分组再构建任何资源：--group 拼错时不应该先去连数据库、启动遥测、写日志。
	group, err := a.workerGroup(opts.Group)
	if err != nil {
		return err
	}
	group, err = group.Subset(opts.Names)
	if err != nil {
		return err
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

	runner, err := worker.NewRunner(group, worker.RunConfig{
		Policy:       a.opts.workerPolicy,
		Concurrency:  a.workerConcurrency(group, opts.Concurrency),
		DrainTimeout: a.opts.workerDrain,
		Logger:       lg,
	})
	if err != nil {
		return a.abort(err)
	}

	pidPath, err := a.resolvePIDPath()
	if err != nil {
		return a.abort(err)
	}
	// The worker process keeps its own pid file: sharing the HTTP one would let
	// `stop` kill the API server and make a second group look like a duplicate
	// instance.
	// worker 进程使用独立 pid 文件：共用 HTTP 的那个会让 `stop` 误杀 API 服务，
	// 也会让第二个分组被误判为重复实例。
	pidPath = workerPIDPath(pidPath, group.Name())
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

	lg.Info("worker group started",
		zap.String("name", a.opts.name),
		zap.String("env", a.opts.env),
		zap.String("group", group.Name()),
		zap.Int("workers", len(group.Workers())),
		zap.Duration("drain_timeout", a.opts.workerDrain),
		zap.Bool("telemetry", telemetry.Enabled()),
	)

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

	a.markReady()
	if health != nil {
		health.setReady()
	}

	// Workers reach the logger, database and tracer through the runtime, the
	// same way a script receives it as an argument.
	// worker 通过 Runtime 访问 logger、数据库与 tracer，方式与脚本接收 Runtime
	// 参数一致。
	runErr := runner.Run(worker.WithRuntime(ctx, rt))
	if health != nil {
		// Keep reporting 503 while the group drains: the process is still alive
		// but it no longer accepts new work.
		// drain 期间继续返回 503：进程还活着，但已经不再接新任务。
		health.setStopping()
	}
	if cerr := a.cleanup(); cerr != nil {
		runErr = errors.Join(runErr, cerr)
	}
	if runErr == nil {
		lg.Info("worker group stopped", zap.String("group", group.Name()))
	} else {
		lg.Error("worker group stopped with error",
			zap.String("group", group.Name()), zap.Error(runErr))
	}
	return runErr
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
