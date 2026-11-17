// Package claim implements row-level task claiming on MySQL 8, so the same
// scan loop can run on every replica without processing a row twice.
// 包 claim 在 MySQL 8 上实现按行认领任务：同一个扫描循环可以跑在每个副本上，
// 而不会把同一行处理两次。
//
// The table is owned by the application: the framework neither migrates nor
// changes it. DDL returns the shape the queries expect, including the index that
// makes SKIP LOCKED cheap; without that index the query degrades into a full
// scan with a growing set of row locks.
// 表由应用自己维护：框架既不迁移也不改结构。DDL 返回查询所依赖的表结构与索引，
// 缺少该索引时 SKIP LOCKED 会退化成全表扫描并持有大量行锁。
package claim

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Task status values stored in the status column.
// 任务在 status 列中的取值。
const (
	// StatusPending is a row waiting to be claimed.
	// StatusPending 表示等待认领（待处理）。
	StatusPending int8 = 0
	// StatusClaimed is a row currently owned by one worker.
	// StatusClaimed 表示已被某个 worker 认领(进行中)。
	StatusClaimed int8 = 1
	// StatusDone is a finished row.
	// StatusDone 表示已完成（已处理完成）。
	StatusDone int8 = 2
	// StatusDead is a row that used up its attempts and needs a human.
	// StatusDead 表示重试次数耗尽、最终还是失败，需要人工介入(处理失败)。
	StatusDead int8 = 3
)

// Defaults applied when the matching Config field is zero.
// 当对应 Config 字段为零时使用的默认值。
const (
	// DefaultBatch is deliberately small. One instance handles a batch serially,
	// so a big batch does not increase throughput — it only lengthens the tail
	// that waits behind the head, and holds more rows locked (or leased) by one
	// worker at a time. Raise Concurrency instead of the batch when one instance
	// must do more work in parallel.
	// DefaultBatch 故意取得小。单实例是串行处理一批的，大批次不会提高吞吐，只会拉长排队尾巴，
	// 并让一个 worker 同时锁住/租住更多行。单实例要更并行应该加并发，而不是加批大小。
	DefaultBatch = 10
	// MaxBatch is the hard ceiling on a claim size; New and Claim reject
	// anything above it. With Runner re-arming each row right before its handler
	// starts, an oversized batch is not incorrect — it just wastes a reap cycle
	// for every row still queued when the shared deadline expires.
	// MaxBatch 是认领批大小的硬上限，New 与 Claim 都会拒绝超过它的值。Runner 在每行 handler
	// 开始前会重新武装租约，所以超大批次并不会算错——只是共享截止时间到期时，还在排队的行每人
	// 白等一个回收周期。
	MaxBatch           = 20
	DefaultLease       = 60 * time.Second
	DefaultMaxAttempts = 5
	DefaultBackoff     = 10 * time.Second
	DefaultMaxBackoff  = 10 * time.Minute
)

var (
	// ErrDBRequired means New was called without a database handle.
	// ErrDBRequired 表示调用 New 时没有提供数据库句柄。
	ErrDBRequired = errors.New("claim: database is required")

	// ErrInvalidTable means the table name is empty or not an identifier.
	// ErrInvalidTable 表示表名为空或不是合法标识符。
	ErrInvalidTable = errors.New("claim: table must be a plain identifier, optionally schema-qualified")

	// ErrJobRequired means no job name was configured.
	// ErrJobRequired 表示没有配置 job 名。
	ErrJobRequired = errors.New("claim: job is required")

	// ErrOwnerRequired means Claim was called without an owner.
	// ErrOwnerRequired 表示调用 Claim 时没有提供 owner。
	ErrOwnerRequired = errors.New("claim: owner is required")

	// ErrBatchTooLarge means the configured or requested claim size exceeds
	// MaxBatch. It is returned instead of silently clamping: a batch someone
	// typed as 200 is a misunderstanding of the serial model, and quietly
	// shrinking it would hide that.
	// ErrBatchTooLarge 表示配置或请求的认领批大小超过 MaxBatch。这里返回错误而不是静默截断：
	// 有人写成 200 说明他误解了串行模型，悄悄改小只会掩盖这一点。
	ErrBatchTooLarge = fmt.Errorf("claim: batch exceeds the maximum of %d", MaxBatch)

	// ErrLeaseLost means the row no longer belongs to the caller: another owner
	// took it over after a lease expired, or the same owner claimed it again
	// after a reap. The handler must not retry its side effects.
	// ErrLeaseLost 表示该行已不属于调用方：租约过期后被其它 owner 接管，或同一
	// owner 在 Reap 之后再次认领。业务不得重试其副作用。
	ErrLeaseLost = errors.New("claim: lease lost")

	// ErrInvalidTask means Succeed, Fail or Dead received a task without the
	// owner and attempts values that Claim returned. Those two fields are the
	// fencing pair; a hand-built task cannot prove it still owns the row.
	// ErrInvalidTask 表示 Succeed、Fail、Dead 收到的 task 缺少 Claim 返回的 owner
	// 与 attempts。这两个字段是围栏组合，手工拼出来的 task 无法证明自己仍持有该行。
	ErrInvalidTask = errors.New("claim: task must carry the owner and attempts returned by claim")

	// ErrInvalidStatus means Succeed or Fail saw a row in an unexpected state.
	// ErrInvalidStatus 表示 Succeed 或 Fail 遇到非预期状态的行。
	ErrInvalidStatus = errors.New("claim: task is not in the claimed state")

	// ErrInvalidPruneWindow means Prune received a non-positive window, which
	// would delete every settled row immediately.
	// ErrInvalidPruneWindow 表示 Prune 收到的窗口不是正数，那会立刻删除全部终态行。
	ErrInvalidPruneWindow = errors.New("claim: prune window must be positive")
)

// identifierPattern keeps table names from carrying SQL. Only the table name is
// interpolated; every value is bound as a parameter.
// identifierPattern 防止表名夹带 SQL。只有表名是拼接的，所有值都走参数绑定。
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config describes one queue: which table, which job and how aggressive the
// claim and retry policy are.
// Config 描述一个队列：用哪张表、哪个 job，以及认领与重试策略。
type Config struct {
	// Table is the task table, optionally schema-qualified ("app.job_task").
	// Table 是任务表，可以带库名前缀（"app.job_task"）。
	Table string
	// Job tags rows so one table can host several task types. Go keeps the
	// name Job; the column it selects is job_type.
	// Job 用于标记行，一张表因此可以承载多种任务。Go 侧保留 Job 这个名字，它选中的列是
	// job_type。
	Job string
	// Batch is the default claim size; zero means DefaultBatch, and anything
	// above MaxBatch is rejected by New with ErrBatchTooLarge.
	// Batch 是默认认领批大小，0 表示 DefaultBatch；超过 MaxBatch 会被 New 以
	// ErrBatchTooLarge 拒绝。
	Batch int
	// Lease is how long a claim stays valid; zero means DefaultLease. Runner
	// re-arms it right before each handler starts, so it must exceed the longest
	// legitimate handler duration (p99 plus margin) — batch size no longer
	// multiplies this requirement. Re-arming protects the queueing window only:
	// a handler that outruns the lease can still be taken over mid-flight and
	// executed again, so handlers must be idempotent. NewRunner rejects
	// HandlerTimeout >= Lease as the enforceable part of that contract.
	// Lease 是认领有效期，0 表示 DefaultLease。Runner 会在每个 handler 开始前重新武装它，
	// 因此它只需大于单次 handler 的最长合法耗时（p99 加余量）——批大小不再放大这个要求。重新
	// 武装只保护排队窗口：跑过租约的 handler 仍可能在执行途中被接管并再跑一遍，所以 handler 必须
	// 幂等。NewRunner 会拒绝 HandlerTimeout >= Lease，这是该约束中可强制的一半。
	Lease time.Duration
	// MaxAttempts caps how many times a row may be claimed, so it bounds the
	// number of executions, not the number of retries: the first claim is
	// attempt 1, and MaxAttempts=1 means the row runs at most once — and
	// possibly zero times, because a process that dies right after claiming
	// leaves attempts at 1, so Reap parks the row as dead without the handler
	// ever running. Failed attempts and takeovers after an expired lease share
	// this budget; a row with rerun_allowed=0 is capped at one execution
	// regardless of this value. Zero means DefaultMaxAttempts.
	// MaxAttempts 限制一行最多被认领几次，因此它限制的是执行次数而不是重试次数：第一次认领
	// 就是 attempts=1，MaxAttempts=1 表示这行最多执行一次——也可能零次：认领后进程立刻崩溃时
	// attempts 已经是 1，Reap 会在 handler 从未运行的情况下把行置为 dead。失败重试与租约过期
	// 后的接管共用这份预算；rerun_allowed=0 的行无论这里配多大都只执行一次。0 表示
	// DefaultMaxAttempts。
	MaxAttempts int
	// Backoff is the first retry delay after a failure; zero means
	// DefaultBackoff.
	// Backoff 是失败后的首次重试延迟，0 表示 DefaultBackoff。
	Backoff time.Duration
	// MaxBackoff caps the exponential retry growth; zero means DefaultMaxBackoff.
	// MaxBackoff 是重试延迟的指数上限，0 表示 DefaultMaxBackoff。
	MaxBackoff time.Duration
	// Hooks receives claim, reap and lease-lost events for metrics; nil
	// disables them.
	// Hooks 接收认领、回收与租约丢失事件，供指标使用；nil 表示不启用。
	Hooks Hooks
}

// Hooks observe queue activity without binding the package to a metrics
// library. Callbacks run on the goroutine that performed the operation and must
// not block.
// Hooks 观察队列活动，但不把包绑定到具体监控库。回调在操作所在的 goroutine 上
// 执行，不得阻塞。
type Hooks struct {
	// OnClaim fires after a claim attempt, including an empty one.
	// OnClaim 在每次认领后触发，包括没有认领到的情况。
	OnClaim func(job string, claimed int)
	// OnReap fires after expired leases were collected.
	// OnReap 在回收过期租约后触发。
	OnReap func(job string, reaped, dead int64)
	// OnLeaseLost fires when Succeed or Fail finds the row taken over.
	// OnLeaseLost 在 Succeed 或 Fail 发现行已被接管时触发。
	OnLeaseLost func(job string)
}

func (c Config) withDefaults() Config {
	c.Table = strings.TrimSpace(c.Table)
	c.Job = strings.TrimSpace(c.Job)
	if c.Batch <= 0 {
		c.Batch = DefaultBatch
	}
	if c.Lease <= 0 {
		c.Lease = DefaultLease
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = DefaultMaxAttempts
	}
	if c.Backoff <= 0 {
		c.Backoff = DefaultBackoff
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = DefaultMaxBackoff
	}
	if c.MaxBackoff < c.Backoff {
		c.MaxBackoff = c.Backoff
	}
	return c
}

// backoffFor returns the delay before the next attempt of a row that has
// already been attempted n times.
// backoffFor 返回已尝试 n 次的行在下一次尝试前的等待时间。
func backoffFor(base, max time.Duration, attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := base
	for i := 1; i < attempts; i++ {
		if d >= max {
			break
		}
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// ceilSeconds turns a duration into whole seconds, rounded up. It is the unit
// the DDL's DATETIME columns store, and rounding up keeps a short lease or
// backoff from becoming zero after MySQL rounds the value on write.
// ceilSeconds 把时长换算成向上取整的整秒。这是 DDL 的 DATETIME 列能存的单位；向上取整保证
// 短租约或短退避不会在 MySQL 写入舍入后变成 0。
func ceilSeconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	secs := int64(d / time.Second)
	if d%time.Second != 0 {
		secs++
	}
	return secs
}

// Queue claims rows of one job from one table.
// Queue 从一张表里认领一个 job 的行。
type Queue struct {
	db             *gorm.DB
	job            string
	table          string
	batch          int
	lease          time.Duration
	maxAttempts    int
	backoff        time.Duration
	maxBackoff     time.Duration
	hooks          Hooks
	selectSQL      string
	updateSQL      string
	extendSQL      string
	ownsSQL        string
	succeedSQL     string
	failSQL        string
	deadSQL        string
	deadNoRerunSQL string
	reapSQL        string
	reapDeadSQL    string
	reapNoRerunSQL string
	pruneSQL       string
}

// New validates the config and builds the queue.
// New 校验配置并构造队列。
func New(db *gorm.DB, cfg Config) (*Queue, error) {
	if db == nil {
		return nil, ErrDBRequired
	}
	cfg = cfg.withDefaults()
	if cfg.Batch > MaxBatch {
		return nil, fmt.Errorf("%w: configured %d", ErrBatchTooLarge, cfg.Batch)
	}
	if cfg.Job == "" {
		return nil, ErrJobRequired
	}
	table, err := quoteTable(cfg.Table)
	if err != nil {
		return nil, err
	}
	q := &Queue{
		db:          db,
		job:         cfg.Job,
		table:       table,
		batch:       cfg.Batch,
		lease:       cfg.Lease,
		maxAttempts: cfg.MaxAttempts,
		backoff:     cfg.Backoff,
		maxBackoff:  cfg.MaxBackoff,
		hooks:       cfg.Hooks,
	}
	// FOR UPDATE SKIP LOCKED is what makes several replicas safe: rows another
	// transaction already locked are skipped instead of waited for.
	// FOR UPDATE SKIP LOCKED 是多个副本安全的前提：别的事务已锁定的行会被跳过，
	// 而不是排队等待。
	// The Go API keeps the name Job; the column is job_type because a job is a
	// type of work, not a link. Only these statements know the spelling.
	// Go 侧仍叫 Job；列名是 job_type，因为它表示工作类型而不是一份工作。只有这些语句
	// 需要知道列的实际拼写。
	q.selectSQL = fmt.Sprintf(`SELECT id, job_type, payload, attempts
  FROM %s
 WHERE job_type = ? AND status = %d
   AND (next_run_at IS NULL OR next_run_at <= NOW(3))
 ORDER BY next_run_at, id
 LIMIT ?
 FOR UPDATE SKIP LOCKED`, table, StatusPending)
	// The column is plain DATETIME, so MySQL would round a sub-second interval
	// to zero or one second depending on the current millisecond — a 200 ms
	// backoff could retry immediately. Round up in Go instead: the deadline is
	// never earlier than requested, and the SQL stays the same for DATETIME(3)
	// tables.
	// 列是普通 DATETIME，亚秒区间会被 MySQL 按当前毫秒随机舍入到 0 或 1 秒——200ms 的退避
	// 可能变成立刻重试。改在 Go 侧向上取整：截止时间不会早于请求值，对 DATETIME(3) 的表
	// 也同样适用。
	q.updateSQL = fmt.Sprintf(`UPDATE %s
   SET status = %d, owner = ?, attempts = attempts + 1,
       start_handle_time = CURRENT_TIMESTAMP,
       lease_until = DATE_ADD(CURRENT_TIMESTAMP, INTERVAL ? SECOND)
 WHERE id IN (?)`, table, StatusClaimed)
	// Extend re-arms one row's lease right before its handler starts. Claim stamps
	// every row in a batch with the same lease_until, but Runner executes them
	// serially: a row near the back can wait past that deadline. Re-arming from
	// the moment of execution gives it a full lease window, and the owner/status/
	// attempts fence makes a row already reaped by another replica fail the CAS —
	// so a stale handler is skipped before it can produce a side effect.
	// Extend 在每条任务执行前重新武装它的租约。Claim 给整批行写同一个 lease_until，但 Runner
	// 串行执行：靠后的行可能还没轮到就已过期。从执行时刻重新计时给它完整的租约窗口；owner/
	// status/attempts 围栏让已被其它副本回收的行 CAS 失败——于是过期 handler 在产生副作用之前
	// 就被跳过。
	q.extendSQL = fmt.Sprintf(`UPDATE %s
   SET lease_until = DATE_ADD(CURRENT_TIMESTAMP, INTERVAL ? SECOND)
 WHERE id = ? AND owner = ? AND status = %d AND attempts = ?`, table, StatusClaimed)
	// MySQL reports rows changed, not rows matched (unless CLIENT_FOUND_ROWS is
	// set, which we cannot assume). Re-arming within the same second can produce
	// the same lease_until and thus affect zero rows. ownsSQL lets Extend tell a
	// no-op update from a lost claim: re-read the fence and check it still holds.
	// MySQL 默认返回被修改的行数而非匹配行数（除非设了 CLIENT_FOUND_ROWS，我们不能假设）。
	// 同一秒内续租可能算出相同的 lease_until，导致 affected=0。ownsSQL 让 Extend 能区分
	// "空更新"与"租约已丢"：重读围栏字段，检查是否仍成立。
	q.ownsSQL = fmt.Sprintf(`SELECT owner, status, attempts FROM %s WHERE id = ?`, table)
	// Every completion statement is fenced by owner, status and attempts.
	//
	// attempts is the per-claim fencing token: Claim increments it on every
	// takeover, so a handler that was superseded — lease expired, row reaped,
	// claimed again by the same worker or by another one — writes with a stale
	// number and matches no row. Owner alone cannot do that: the same worker
	// instance claims the same row again with the same owner string.
	//
	// The statements intentionally do not compare lease_until with NOW(3): a
	// handler that finishes just after its lease expired but before Reap ran
	// still owns the row (nothing else could have taken it), and completing it
	// is better than failing a task whose side effects already happened. Once
	// Reap has run, attempts has moved on and this CAS rejects the stale write.
	//
	// 每条收尾语句都由 owner、status 与 attempts 三重围栏。
	//
	// attempts 就是"每次认领"的围栏值：Claim 在每次接管时自增，因此被取代的
	// handler——租约过期、行被 Reap、又被同一个 worker 或别的 worker 认领——带着
	// 过期的序号写回时匹配不到任何行。仅靠 owner 做不到这一点：同一个 worker
	// 实例会用同样的 owner 字符串再次认领同一行。
	//
	// 这些语句刻意不与 NOW(3) 比较 lease_until：租约刚过期但 Reap 还没跑的
	// handler 仍持有该行（没有别人能拿走），让它完成比判定失败更合理，毕竟副作用
	// 已经发生。Reap 一旦跑过，attempts 就变了，这个 CAS 会拒绝过期写回。
	// Succeed clears the last failure reason: a done row still carrying an old
	// reason reads as a failed one to whoever queries the table later.
	// Succeed 会清掉上一次的失败原因：否则已完成的行还带着旧的失败原因，后来查表的人会
	// 以为它失败了。
	q.succeedSQL = fmt.Sprintf(`UPDATE %s
   SET status = %d, owner = '', lease_until = NULL, fail_reason = NULL,
       finish_handle_time = CURRENT_TIMESTAMP
 WHERE id = ? AND owner = ? AND status = %d AND attempts = ?`, table, StatusDone, StatusClaimed)
	q.failSQL = fmt.Sprintf(`UPDATE %s
   SET status = %d, owner = '', lease_until = NULL, fail_reason = ?,
       fail_handle_time = CURRENT_TIMESTAMP,
       next_run_at = DATE_ADD(CURRENT_TIMESTAMP, INTERVAL ? SECOND)
 WHERE id = ? AND owner = ? AND status = %d AND attempts = ? AND rerun_allowed = 1`, table, StatusPending, StatusClaimed)
	q.deadSQL = fmt.Sprintf(`UPDATE %s
   SET status = %d, owner = '', lease_until = NULL, fail_reason = ?,
       fail_handle_time = CURRENT_TIMESTAMP
 WHERE id = ? AND owner = ? AND status = %d AND attempts = ?`, table, StatusDead, StatusClaimed)
	// A row with rerun_allowed = 0 may run once and only once. A handler error is
	// still an execution, so it is parked rather than rescheduled. This statement
	// runs after failSQL matched nothing: the row is still ours, it just refuses
	// another run.
	// rerun_allowed=0 的行只允许执行一次。handler 报错也算执行过一次，因此置为 dead
	// 而不是重新排期。该语句在 failSQL 未命中后执行：行仍属于我们，只是拒绝再跑。
	q.deadNoRerunSQL = fmt.Sprintf(`UPDATE %s
   SET status = %d, owner = '', lease_until = NULL, fail_reason = ?,
       fail_handle_time = CURRENT_TIMESTAMP
 WHERE id = ? AND owner = ? AND status = %d AND attempts = ? AND rerun_allowed = 0`, table, StatusDead, StatusClaimed)
	q.reapSQL = fmt.Sprintf(`UPDATE %s
   SET status = %d, owner = '', lease_until = NULL
 WHERE job_type = ? AND status = %d AND lease_until IS NOT NULL AND lease_until < NOW(3)
   AND attempts < ? AND rerun_allowed = 1`, table, StatusPending, StatusClaimed)
	q.reapDeadSQL = fmt.Sprintf(`UPDATE %s
   SET status = %d, owner = '', lease_until = NULL, fail_reason = ?,
       fail_handle_time = CURRENT_TIMESTAMP
 WHERE job_type = ? AND status = %d AND lease_until IS NOT NULL AND lease_until < NOW(3)
   AND attempts >= ? AND rerun_allowed = 1`, table, StatusDead, StatusClaimed)
	// The other half of the expired-lease split: a row that refuses another run
	// cannot go back to pending, so an expired lease parks it. The two reap
	// statements are mutually exclusive through rerun_allowed, and this one does
	// not test attempts — the cap is irrelevant for a row that never reruns.
	// 过期租约的另一半：拒绝重跑的行不能回到待认领，租约一过期就置为 dead。两条回收语句
	// 靠 rerun_allowed 互斥；这一条不判断 attempts——对不会重跑的行来说上限没有意义。
	q.reapNoRerunSQL = fmt.Sprintf(`UPDATE %s
   SET status = %d, owner = '', lease_until = NULL, fail_reason = ?,
       fail_handle_time = CURRENT_TIMESTAMP
 WHERE job_type = ? AND status = %d AND lease_until IS NOT NULL AND lease_until < NOW(3)
   AND rerun_allowed = 0`, table, StatusDead, StatusClaimed)
	// A settled row's updated_at is its settlement time: Succeed/Fail change
	// status, and the ON UPDATE column follows. The age comparison therefore
	// stays in the database clock, and a row that becomes settled again later
	// is judged by the new moment. Pending and claimed rows are never targeted.
	// 终态行的 updated_at 就是收尾时刻：Succeed/Fail 改状态时会刷新 ON UPDATE 列。
	// 年龄比较因此留在数据库时钟里；之后又被重新收尾的行按新时刻判断。pending 与
	// claimed 行永远不会被选中。
	q.pruneSQL = fmt.Sprintf(`DELETE FROM %s
 WHERE job_type = ? AND status IN (%d, %d)
   AND updated_at < DATE_SUB(NOW(3), INTERVAL ? SECOND)`, table, StatusDone, StatusDead)
	return q, nil
}

// Job returns the configured job name.
// Job 返回配置的 job 名。
func (q *Queue) Job() string { return q.job }

// Batch returns the default claim size.
// Batch 返回默认认领批大小。
func (q *Queue) Batch() int { return q.batch }

// Lease returns the claim lease.
// Lease 返回认领租约时长。
func (q *Queue) Lease() time.Duration { return q.lease }

// MaxAttempts returns the attempt cap.
// MaxAttempts 返回尝试次数上限。
func (q *Queue) MaxAttempts() int { return q.maxAttempts }

// Task is one claimed row.
//
// Owner and Attempts are the fencing pair: every completion statement compares
// both, so a Task must be passed on exactly as Claim returned it. Attempts is
// what distinguishes "this claim" from "the previous claim by the same owner".
// Task 是一条被认领的行。
//
// Owner 与 Attempts 组成围栏：每条收尾语句都会比较这两个值，因此 Task 必须按
// Claim 返回的原样传下去。Attempts 区分的正是"本次认领"与"同一 owner 的上一次
// 认领"。
type Task struct {
	ID       uint64
	Job      string
	Payload  []byte
	Attempts int
	Owner    string
}

// Claim locks and marks up to n rows for owner. It returns an empty slice when
// nothing is due; callers should back off instead of spinning. n <= 0 means the
// configured Batch, and n > MaxBatch is rejected with ErrBatchTooLarge.
//
// Rows another transaction is already working on are skipped, so every replica
// can run the same loop. The claim and the update happen in one transaction: a
// crash between them would otherwise leave rows claimed by nobody.
// Claim 为 owner 锁定并标记最多 n 行；没有到期任务时返回空切片，调用方应退避
// 而不是空转。n <= 0 表示用配置的 Batch，n > MaxBatch 会以 ErrBatchTooLarge 拒绝。
//
// 其它事务正在处理的行会被跳过，因此每个副本都能跑同一个循环。认领与更新在同一
// 个事务里完成，否则中途崩溃会留下无人负责的行。
func (q *Queue) Claim(ctx context.Context, owner string, n int) ([]Task, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, ErrOwnerRequired
	}
	if n <= 0 {
		n = q.batch
	} else if n > MaxBatch {
		// An explicit n comes from a caller, not from validated config, so it
		// gets checked here as well: MaxBatch is the ceiling no matter where the
		// number came from.
		// 显式传的 n 来自调用方而不是已验证的配置，因此这里也要校验：不管数字从哪来，
		// MaxBatch 都是上限。
		return nil, fmt.Errorf("%w: requested %d", ErrBatchTooLarge, n)
	}

	var tasks []Task
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(q.selectSQL, q.job, n).Scan(&tasks).Error; err != nil {
			return fmt.Errorf("claim: select: %w", err)
		}
		if len(tasks) == 0 {
			return nil
		}
		ids := make([]uint64, len(tasks))
		for i := range tasks {
			ids[i] = tasks[i].ID
		}
		if err := tx.Exec(q.updateSQL, owner, ceilSeconds(q.lease), ids).Error; err != nil {
			return fmt.Errorf("claim: update: %w", err)
		}
		for i := range tasks {
			tasks[i].Owner = owner
			tasks[i].Attempts++
			tasks[i].Job = q.job
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	q.claimHook(len(tasks))
	return tasks, nil
}

// Extend re-arms one claimed row's lease for a fresh full Lease window, before
// its handler starts. Runner calls it for every task in a batch because Claim
// stamped them all with the same deadline; without this, a row waiting behind
// slow siblings could be reaped and run by another replica. The CAS on
// owner + status + attempts means a row already reaped fails with ErrLeaseLost,
// and the caller must skip the handler — its side effects must not run twice.
// Extend 在 handler 开始前把一条已认领行的租约重新武装为一个完整的 Lease 窗口。Runner 对批次中
// 每条任务都调用它，因为 Claim 给所有行写了同一个截止时间；否则排在慢任务后面的行可能被回收、
// 被另一个副本执行。owner + status + attempts 的 CAS 保证已被回收的行返回 ErrLeaseLost，
// 调用方必须跳过 handler——它的副作用绝不能跑两遍。
func (q *Queue) Extend(ctx context.Context, task Task) error {
	owner, err := claimFields(task)
	if err != nil {
		return err
	}
	res := q.db.WithContext(ctx).Exec(q.extendSQL, ceilSeconds(q.lease), task.ID, owner, task.Attempts)
	if res.Error != nil {
		return fmt.Errorf("claim: extend: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		return nil
	}
	// Zero rows changed can mean either "the row was taken over" or "the new
	// lease_until equals the old one" (DATETIME seconds, same-second re-arm).
	// Re-read the fence: only a matching owner/status/attempts means we still
	// own the row and the no-op update is harmless.
	// 0 行被修改可能是"已被接管"，也可能是"新 lease_until 与旧值相同"（DATETIME 秒精度、
	// 同一秒内续租）。重读围栏：只有 owner/status/attempts 都匹配才算仍持有，空更新无害。
	var cur struct {
		Owner    string
		Status   int8
		Attempts int
	}
	if err := q.db.WithContext(ctx).Raw(q.ownsSQL, task.ID).Scan(&cur).Error; err != nil {
		return fmt.Errorf("claim: extend verify: %w", err)
	}
	if cur.Owner == owner && cur.Status == StatusClaimed && cur.Attempts == task.Attempts {
		return nil
	}
	return fmt.Errorf("%w: task %d", ErrLeaseLost, task.ID)
}

// Succeed closes a task. ErrLeaseLost means the row was taken over after this
// claim, so the handler must not repeat its side effects.
// Succeed 结束任务。返回 ErrLeaseLost 表示该行在本次认领之后已被接管，业务不得
// 重复其副作用。
func (q *Queue) Succeed(ctx context.Context, task Task) error {
	owner, err := claimFields(task)
	if err != nil {
		return err
	}
	res := q.db.WithContext(ctx).Exec(q.succeedSQL, task.ID, owner, task.Attempts)
	if res.Error != nil {
		return fmt.Errorf("claim: succeed: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		q.leaseLostHook()
		return fmt.Errorf("%w: task %d", ErrLeaseLost, task.ID)
	}
	return nil
}

// Fail records an attempt and schedules a retry with exponential backoff, or
// moves the task to the dead state once MaxAttempts is reached. It returns
// ErrLeaseLost when the row was taken over after this claim.
//
// The dead return says the row was parked rather than rescheduled — either the
// attempt cap was reached or the row carries rerun_allowed = 0. Callers use it
// to tell "gave up" from "will try again" without a second query.
// Fail 记录一次失败并按指数退避安排重试；达到 MaxAttempts 后转为死亡态。若该行
// 在本次认领之后已被接管，返回 ErrLeaseLost。
//
// dead 返回值表示行被置死而非重新排期：要么尝试次数用尽，要么该行的 rerun_allowed=0。
// 调用方据此区分"放弃了"和"还会再试"，不需要再查一次库。
func (q *Queue) Fail(ctx context.Context, task Task, cause error) (dead bool, err error) {
	owner, err := claimFields(task)
	if err != nil {
		return false, err
	}
	// Attempts was incremented at claim time, so reaching the cap here means
	// this failure was the last allowed try.
	// attempts 在认领时已自增，因此这里达到上限意味着本次失败就是最后一次尝试。
	reason := failureReason(cause)
	if task.Attempts >= q.maxAttempts {
		// The reason sits in the task row rather than only in a log line, so a
		// dead row answers "why" when somebody opens the table.
		// 失败原因落到任务行而不只是日志里，这样打开表就能知道一条 dead 行为什么失败。
		res := q.db.WithContext(ctx).Exec(q.deadSQL, reason, task.ID, owner, task.Attempts)
		if res.Error != nil {
			return false, fmt.Errorf("claim: dead: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			q.leaseLostHook()
			return false, fmt.Errorf("%w: task %d", ErrLeaseLost, task.ID)
		}
		return true, nil
	}
	delay := backoffFor(q.backoff, q.maxBackoff, task.Attempts)
	res := q.db.WithContext(ctx).Exec(q.failSQL, reason, ceilSeconds(delay), task.ID, owner, task.Attempts)
	if res.Error != nil {
		return false, fmt.Errorf("claim: fail: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		return false, nil
	}
	// failSQL matched nothing for one of two reasons, and they need opposite
	// answers: the row refuses another run (rerun_allowed = 0), or it is no
	// longer ours. Park it in the first case; report ErrLeaseLost in the second.
	// The no-rerun statement carries the same owner/status/attempts fence, so it
	// can only match while the row is still ours.
	// failSQL 未命中只有两种可能，而它们要用相反的方式处理：行拒绝再跑（rerun_allowed=0），
	// 或行已不属于我们。前者置死，后者报 ErrLeaseLost。该语句带同样的 owner/status/attempts
	// 围栏，因此只有行仍属于我们时才会命中。
	res = q.db.WithContext(ctx).Exec(q.deadNoRerunSQL, reason, task.ID, owner, task.Attempts)
	if res.Error != nil {
		return false, fmt.Errorf("claim: dead (no rerun): %w", res.Error)
	}
	if res.RowsAffected > 0 {
		return true, nil
	}
	q.leaseLostHook()
	return false, fmt.Errorf("%w: task %d", ErrLeaseLost, task.ID)
}

// failureReason stores a bounded cause in the task row. A TEXT column could
// hold a whole stack trace, but that would turn every failed row into a blob;
// the rune-aware cut also keeps a Chinese error from ending mid-character.
// failureReason 把有界的错误原因写进任务行。TEXT 列装得下整段堆栈，但那会让每条失败行
// 变成一个文本块；按 rune 截断也避免中文错误在半个字上被切断。
func failureReason(cause error) string {
	if cause == nil {
		return ""
	}
	const max = 2048
	runes := []rune(cause.Error())
	if len(runes) > max {
		return string(runes[:max])
	}
	return string(runes)
}

// Reasons Reap writes on rows it parks. A dead row that only says "lease
// expired" cannot tell an operator whether the task failed or simply never got
// a second chance, and the two need different follow-up.
// Reap 置死时写入的原因。只写"租约过期"分不清是任务真失败还是根本没拿到第二次机会，
// 而这两种情况的人工处理方式不同。
const (
	reasonAttemptsExhausted = "attempts exhausted after the lease expired"
	reasonNoRerun           = "lease expired and rerun_allowed = 0"
)

// claimFields validates the fencing pair a completion needs.
// claimFields 校验收尾所需的围栏组合。
func claimFields(task Task) (string, error) {
	owner := strings.TrimSpace(task.Owner)
	if owner == "" {
		return "", ErrOwnerRequired
	}
	if task.Attempts < 1 {
		return "", fmt.Errorf("%w: task %d", ErrInvalidTask, task.ID)
	}
	return owner, nil
}

// Reap returns expired leases to the pending state and moves exhausted rows to
// the dead state. It is safe to call from every replica: the UPDATE only
// touches rows whose lease already expired.
//
// Rows with rerun_allowed = 0 never go back to pending: an expired lease parks
// them as dead instead, because nobody may run them a second time. That is the
// at-most-once half of the switch — see DDL.
// Reap 把租约过期的任务放回待处理态，并把重试耗尽的转为死亡态。每个副本都可以
// 调用：UPDATE 只影响租约确已过期的行。
//
// rerun_allowed=0 的行不会回到待认领：租约过期直接置为 dead，因为不允许任何人再跑第二遍。
// 这是该开关 at-most-once 的那一半——见 DDL。
func (q *Queue) Reap(ctx context.Context) (reaped, dead int64, err error) {
	res := q.db.WithContext(ctx).Exec(q.reapSQL, q.job, q.maxAttempts)
	if res.Error != nil {
		return 0, 0, fmt.Errorf("claim: reap: %w", res.Error)
	}
	reaped = res.RowsAffected

	res = q.db.WithContext(ctx).Exec(q.reapDeadSQL, reasonAttemptsExhausted, q.job, q.maxAttempts)
	if res.Error != nil {
		return reaped, 0, fmt.Errorf("claim: reap dead: %w", res.Error)
	}
	dead = res.RowsAffected

	res = q.db.WithContext(ctx).Exec(q.reapNoRerunSQL, reasonNoRerun, q.job)
	if res.Error != nil {
		return reaped, dead, fmt.Errorf("claim: reap no-rerun: %w", res.Error)
	}
	dead += res.RowsAffected

	if q.hooks.OnReap != nil {
		q.hooks.OnReap(q.job, reaped, dead)
	}
	return reaped, dead, nil
}

// Prune deletes this job's settled rows — done or dead — whose settlement is
// older than olderThan, and reports how many disappeared. Pending and claimed
// rows are never touched: they are still somebody's work.
//
// Call it from a scheduled maintenance loop, not from the claim path. Without
// it the table only grows and idx_claim keeps widening; a dead row is worth
// keeping until a human has looked at it, which is what the window is for.
// Prune 删除本 job 中收尾时间早于 olderThan 的终态行（done 或 dead），并返回删除条数。
// pending 与 claimed 行永不触碰：它们仍是某个人的活。
//
// 请从定时维护任务调用，不要放进认领路径。没有它表只增不减，idx_claim 也越来越宽；
// dead 行在人工看过之前值得保留，这正是窗口的用途。
func (q *Queue) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		return 0, ErrInvalidPruneWindow
	}
	res := q.db.WithContext(ctx).Exec(q.pruneSQL, q.job, ceilSeconds(olderThan))
	if res.Error != nil {
		return 0, fmt.Errorf("claim: prune: %w", res.Error)
	}
	return res.RowsAffected, nil
}

func (q *Queue) claimHook(claimed int) {
	if q.hooks.OnClaim != nil {
		q.hooks.OnClaim(q.job, claimed)
	}
}

func (q *Queue) leaseLostHook() {
	if q.hooks.OnLeaseLost != nil {
		q.hooks.OnLeaseLost(q.job)
	}
}

// DDL returns the table definition the queries expect, including the index that
// keeps SKIP LOCKED from degenerating into a full scan. The table name goes
// through the same identifier check as New.
//
// The time columns are plain DATETIME, so a lease or a backoff shorter than one
// second is rounded to whole seconds by MySQL. Claim's SQL still uses NOW(3),
// which compares correctly against both precisions. created_by and deleted_at
// are application columns: claim never reads or writes them, so a table without
// them still works. A non-NULL deleted_at does not hide a row from Claim.
// fail_reason is different: Fail writes the bounded cause there and Succeed
// clears it, so a dead row carries its own explanation. start_handle_time and
// finish_handle_time stamp the transition into status 1 and 2. fail_handle_time
// stamps every failure, including a retryable one that returns the row to
// pending (status 0) — it is the last time the row failed, not a status marker.
//
// rerun_allowed is the business-side switch between at-least-once and
// at-most-once, per row. It defaults to 1: a failed or abandoned row may be
// claimed again. Set it to 0 for work that must never execute twice — claim then
// refuses to reschedule it. A handler error parks the row as dead instead of
// backing off, and an expired lease parks it instead of returning it to pending.
// The row can still be claimed once more if it was already pending, so set the
// column when the row is inserted, not after the first handler starts. claim
// reads the column and never writes it.
// DDL 返回查询所依赖的表结构，包含防止 SKIP LOCKED 退化为全表扫描的索引。表名与
// New 走同一套标识符校验。
//
// 时间列用普通 DATETIME，因此短于一秒的租约或退避会被 MySQL 取整到秒。claim 的 SQL 仍用
// NOW(3)，它与两种精度都能正确比较。start_handle_time / finish_handle_time 记录行最近一次
// 进入 status 1 / 2 的墙钟时间；fail_handle_time 记录最近一次失败时间，包括退避重试回到
// pending（status 0）的情况——它是"上一次失败"的时刻，不是状态标记。created_by 与
// deleted_at 是应用侧字段：claim 从不读写，没有这两列的表照样能用；deleted_at 非空也不会让
// Claim 跳过该行。fail_reason 不同：Fail 会写入有界的错误原因，Succeed 会清空，因此 dead 行
// 本身就带着失败原因。
//
// rerun_allowed 是业务侧的逐行开关，在 at-least-once 与 at-most-once 之间选择。默认 1：
// 失败或被放弃的行可以被再次认领。对绝不能执行两次的活设为 0——claim 就不再重新排期它：
// handler 报错直接置 dead 而不是退避重试，租约过期直接置 dead 而不是放回待认领。仍在待认领
// 状态的行还是可能被认领一次，因此要在插入时就设好这一列，而不是等 handler 跑起来再改。
// claim 只读这一列，从不写它。
func DDL(table string) (string, error) {
	quoted, err := quoteTable(strings.TrimSpace(table))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  job_type    VARCHAR(64)  NOT NULL COMMENT '任务类型',
  status      TINYINT      NOT NULL DEFAULT 0 COMMENT '任务状态：0待认领(待处理) 1已认领(进行中) 2已完成 3处理失败,待人工介入',
  owner       VARCHAR(96)  NOT NULL DEFAULT '' COMMENT '当前认领者',
  lease_until DATETIME     NULL COMMENT '租约到期时间',
  attempts    INT          NOT NULL DEFAULT 0 COMMENT '已认领次数',
  next_run_at DATETIME     NULL COMMENT '下次可执行时间，NULL表示立即可认领',
  rerun_allowed TINYINT    NOT NULL DEFAULT 1 COMMENT '是否允许被再次消费：1允许(失败重试/租约过期后可被重跑)，0只执行一次(失败或租约过期直接转3)',
  payload     JSON         NULL COMMENT '任务载荷',
  fail_reason TEXT         NULL DEFAULT NULL COMMENT '失败原因',
  start_handle_time  DATETIME NULL DEFAULT NULL COMMENT '状态改为1(已认领进行中)的时间',
  finish_handle_time DATETIME NULL DEFAULT NULL COMMENT '状态改为2(已完成)的时间',
  fail_handle_time   DATETIME NULL DEFAULT NULL COMMENT '最近一次失败时间',
  created_by  VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '创建人',
  created_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  deleted_at  DATETIME     NULL DEFAULT NULL COMMENT '删除时间',
  PRIMARY KEY (id),
  KEY idx_claim (job_type, status, next_run_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='认领任务表'`, quoted), nil
}

func quoteTable(table string) (string, error) {
	if table == "" {
		return "", ErrInvalidTable
	}
	parts := strings.Split(table, ".")
	if len(parts) > 2 {
		return "", ErrInvalidTable
	}
	for i, part := range parts {
		if !identifierPattern.MatchString(part) {
			return "", fmt.Errorf("%w: %q", ErrInvalidTable, table)
		}
		parts[i] = "`" + part + "`"
	}
	return strings.Join(parts, "."), nil
}
