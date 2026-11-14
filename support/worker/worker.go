// Package worker hosts long-running tasks in their own process instead of
// riding the HTTP server.
// 包 worker 托管常驻任务：它们运行在独立进程里，不随 HTTP 服务启动。
//
// A group is the deployment unit: every worker in one group lives and dies in
// the same process, so a group maps to one container. Concurrency is a
// goroutine count, not a process count; isolation between groups is the
// scheduler's job, not the framework's.
// 分组是部署单位：同组 worker 在同一进程里同生共死，因此一个分组对应一个容器。
// 并发是 goroutine 数量而非进程数量；分组之间的隔离交给调度器，不由框架模拟。
package worker

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	// ErrNameRequired means a group or worker name is empty.
	// ErrNameRequired 表示分组名或 worker 名为空。
	ErrNameRequired = errors.New("worker: name is required")

	// ErrInvalidName means a name does not match the required pattern.
	// ErrInvalidName 表示名字不符合要求的格式。
	ErrInvalidName = errors.New("worker: name must match ^[a-z][a-z0-9-]{0,62}$")

	// ErrNoWorkers means a group was built without workers.
	// ErrNoWorkers 表示分组里没有任何 worker。
	ErrNoWorkers = errors.New("worker: group has no workers")

	// ErrNilWorker means a nil Worker was passed to a group.
	// ErrNilWorker 表示传给分组的 Worker 是 nil。
	ErrNilWorker = errors.New("worker: worker is nil")

	// ErrDuplicateWorker means two workers in one group share a name.
	// ErrDuplicateWorker 表示同一分组内有两个 worker 用了同一个名字。
	ErrDuplicateWorker = errors.New("worker: duplicate worker name in group")

	// ErrUnknownWorker means a name filter matched nothing.
	// ErrUnknownWorker 表示按名字筛选时没有匹配到 worker。
	ErrUnknownWorker = errors.New("worker: unknown worker")

	// ErrNilRun means Func was called without a run function.
	// ErrNilRun 表示调用 Func 时没有提供运行函数。
	ErrNilRun = errors.New("worker: run function is nil")
)

// namePattern is shared with the framework's module and script names so one
// vocabulary covers commands, modules and workers.
// namePattern 与框架的模块名、脚本名规则一致，让命令、模块与 worker 共用一套
// 命名约定。
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// ValidName reports whether name follows the group and worker naming rule. It
// is exported for CLI validation: `stop --worker` turns the value into a pid
// file name, so a stray slash must be rejected before that.
// ValidName 判断名字是否符合分组与 worker 的命名规则。它导出给 CLI 校验使用：
// `stop --worker` 会把这个值拼进 pid 文件名，拼之前必须先挡掉斜杠之类的字符。
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Worker is one long-running task. Run must block until ctx is cancelled and
// then return nil; returning nil earlier is treated as a contract violation,
// and a non-nil error is handled according to the runner's Policy.
//
// The runner shares one instance across all its concurrent runs, so Run must be
// safe to call concurrently: keep mutable state inside Run, not on the receiver.
// Worker 是一个常驻任务。Run 必须阻塞到 ctx 被取消后返回 nil；提前返回 nil
// 视为违反约定，返回非 nil 则按 Runner 的 Policy 处理。
//
// Runner 会让同一实例的多个 Run 并发执行，因此 Run 必须可并发调用：可变状态放在
// Run 内部，不要挂在接收者上。
type Worker interface {
	// Name is unique inside its group.
	// Name 在分组内唯一。
	Name() string
	// Run owns the loop. It must react to ctx cancellation and release its
	// resources before returning.
	// Run 拥有循环本身：必须响应 ctx 取消，并在返回前释放自己持有的资源。
	Run(ctx context.Context) error
}

// Concurrent is implemented by workers that declare their own instance count.
// It is optional: a Worker without it runs once, and any worker's count can
// still be overridden by RunConfig.Concurrency (WithWorkerConcurrency or the
// --concurrency flag).
// Concurrent 由自带实例数的 worker 实现。它是可选的：没实现的 Worker 只跑 1 个
// 实例；无论实现与否，RunConfig.Concurrency（WithWorkerConcurrency 或
// --concurrency）都能覆盖这个数字。
type Concurrent interface {
	// Concurrency is how many instances the worker wants by default.
	// Concurrency 是这个 worker 默认要跑的实例数。
	Concurrency() uint8
}

// Group is a set of workers that live and die together in one process.
// Group 是一组同生共死的 worker，对应一个进程、一个 Deployment。
type Group struct {
	name    string
	workers []Worker
}

// NewGroup validates and builds a group. The name follows the same pattern as
// module names, workers must be non-nil, uniquely named and at least one.
// NewGroup 校验并构造分组。分组名沿用模块名规则，worker 必须非 nil、名字唯一，
// 且至少有一个。
func NewGroup(name string, workers ...Worker) (*Group, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: group", ErrNameRequired)
	}
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("%w: group %q", ErrInvalidName, name)
	}
	if len(workers) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrNoWorkers, name)
	}

	seen := make(map[string]struct{}, len(workers))
	for i, w := range workers {
		if w == nil {
			return nil, fmt.Errorf("%w: group %q index %d", ErrNilWorker, name, i)
		}
		workerName := strings.TrimSpace(w.Name())
		if workerName == "" {
			return nil, fmt.Errorf("%w: group %q index %d", ErrNameRequired, name, i)
		}
		if !namePattern.MatchString(workerName) {
			return nil, fmt.Errorf("%w: %q in group %q", ErrInvalidName, workerName, name)
		}
		if _, dup := seen[workerName]; dup {
			return nil, fmt.Errorf("%w: %q in group %q", ErrDuplicateWorker, workerName, name)
		}
		// A worker that declares its own count must declare a usable one:
		// Func takes a uint8, so zero is the only invalid value it can hold.
		// 自带实例数的 worker 必须声明一个可用的数字：Func 接收 uint8，因此 0 是它
		// 唯一可能出现的非法值。
		if c, ok := w.(Concurrent); ok && c.Concurrency() == 0 {
			return nil, fmt.Errorf("%w: %q in group %q", ErrInvalidConcurrency, workerName, name)
		}
		seen[workerName] = struct{}{}
	}
	return &Group{name: name, workers: append([]Worker(nil), workers...)}, nil
}

// Name returns the group name.
// Name 返回分组名。
func (g *Group) Name() string { return g.name }

// Workers returns the group members in registration order.
// Workers 按注册顺序返回组成员。
func (g *Group) Workers() []Worker {
	return append([]Worker(nil), g.workers...)
}

// Subset returns a group with only the named workers, keeping the original
// order. It is how `worker --name` narrows a group without rebuilding it.
// Subset 返回只包含指定名字的 worker 的分组，顺序与注册顺序一致；这是
// `worker --name` 缩小启动范围的实现方式，无需重建分组。
func (g *Group) Subset(names []string) (*Group, error) {
	if len(names) == 0 {
		return g, nil
	}
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		wanted[name] = struct{}{}
	}
	selected := make([]Worker, 0, len(wanted))
	for _, w := range g.workers {
		if _, ok := wanted[w.Name()]; ok {
			selected = append(selected, w)
			delete(wanted, w.Name())
		}
	}
	for name := range wanted {
		return nil, fmt.Errorf("%w: %q in group %q", ErrUnknownWorker, name, g.name)
	}
	return NewGroup(g.name, selected...)
}

// Func adapts a function to a Worker and carries the instance count the worker
// wants by default, so a simple task needs neither a one-field struct nor a
// separate host option. A zero count is rejected when the group is built.
// Func 把函数适配成 Worker，并带上这个 worker 默认要跑的实例数，简单任务因此既不
// 必定义结构体，也不必再写一条宿主选项。计数为 0 时在构造分组阶段报错。
func Func(name string, concurrency uint8, run func(ctx context.Context) error) Worker {
	return funcWorker{name: name, concurrency: concurrency, run: run}
}

type funcWorker struct {
	name        string
	concurrency uint8
	run         func(ctx context.Context) error
}

func (w funcWorker) Name() string { return w.name }

// Concurrency reports how many instances the worker wants by default.
// Concurrency 返回这个 worker 默认要跑的实例数。
func (w funcWorker) Concurrency() uint8 { return w.concurrency }

func (w funcWorker) Run(ctx context.Context) error {
	if w.run == nil {
		return fmt.Errorf("%w: %q", ErrNilRun, w.name)
	}
	return w.run(ctx)
}
