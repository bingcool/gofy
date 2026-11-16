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
	DefaultBatch       = 100
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
	// Batch is the default claim size; zero means DefaultBatch.
	// Batch 是默认认领批大小，0 表示 DefaultBatch。
	Batch int
	// Lease is how long a claim stays valid; zero means DefaultLease. Keep it
	// at least three times the p99 handler duration, otherwise a slow handler
	// gets its row taken away.
	// Lease 是认领有效期，0 表示 DefaultLease。它至少应为处理耗时 p99 的 3 倍，
	// 否则慢任务的行会被别人抢走。
	Lease time.Duration
	// MaxAttempts caps how many times a row may be claimed; zero means
	// DefaultMaxAttempts.
	// MaxAttempts 限制一行最多被认领几次，0 表示 DefaultMaxAttempts。
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
	db          *gorm.DB
	job         string
	table       string
	batch       int
	lease       time.Duration
	maxAttempts int
	backoff     time.Duration
	maxBackoff  time.Duration
	hooks       Hooks
	selectSQL   string
	updateSQL   string
	succeedSQL  string
	failSQL     string
	deadSQL     string
	reapSQL     string
	reapDeadSQL string
}

// New validates the config and builds the queue.
// New 校验配置并构造队列。
func New(db *gorm.DB, cfg Config) (*Queue, error) {
	if db == nil {
		return nil, ErrDBRequired
	}
	cfg = cfg.withDefaults()
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
       lease_until = DATE_ADD(CURRENT_TIMESTAMP, INTERVAL ? SECOND)
 WHERE id IN (?)`, table, StatusClaimed)
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
   SET status = %d, owner = '', lease_until = NULL, fail_resion = NULL
 WHERE id = ? AND owner = ? AND status = %d AND attempts = ?`, table, StatusDone, StatusClaimed)
	q.failSQL = fmt.Sprintf(`UPDATE %s
   SET status = %d, owner = '', lease_until = NULL, fail_resion = ?,
       next_run_at = DATE_ADD(CURRENT_TIMESTAMP, INTERVAL ? SECOND)
 WHERE id = ? AND owner = ? AND status = %d AND attempts = ?`, table, StatusPending, StatusClaimed)
	q.deadSQL = fmt.Sprintf(`UPDATE %s
   SET status = %d, owner = '', lease_until = NULL, fail_resion = ?
 WHERE id = ? AND owner = ? AND status = %d AND attempts = ?`, table, StatusDead, StatusClaimed)
	q.reapSQL = fmt.Sprintf(`UPDATE %s
   SET status = %d, owner = '', lease_until = NULL
 WHERE job_type = ? AND status = %d AND lease_until IS NOT NULL AND lease_until < NOW(3)
   AND attempts < ?`, table, StatusPending, StatusClaimed)
	q.reapDeadSQL = fmt.Sprintf(`UPDATE %s
   SET status = %d, owner = '', lease_until = NULL
 WHERE job_type = ? AND status = %d AND lease_until IS NOT NULL AND lease_until < NOW(3)
   AND attempts >= ?`, table, StatusDead, StatusClaimed)
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
// nothing is due; callers should back off instead of spinning.
//
// Rows another transaction is already working on are skipped, so every replica
// can run the same loop. The claim and the update happen in one transaction: a
// crash between them would otherwise leave rows claimed by nobody.
// Claim 为 owner 锁定并标记最多 n 行；没有到期任务时返回空切片，调用方应退避
// 而不是空转。
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
// Fail 记录一次失败并按指数退避安排重试；达到 MaxAttempts 后转为死亡态。若该行
// 在本次认领之后已被接管，返回 ErrLeaseLost。
func (q *Queue) Fail(ctx context.Context, task Task, cause error) error {
	owner, err := claimFields(task)
	if err != nil {
		return err
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
			return fmt.Errorf("claim: dead: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			q.leaseLostHook()
			return fmt.Errorf("%w: task %d", ErrLeaseLost, task.ID)
		}
		return nil
	}
	delay := backoffFor(q.backoff, q.maxBackoff, task.Attempts)
	res := q.db.WithContext(ctx).Exec(q.failSQL, reason, ceilSeconds(delay), task.ID, owner, task.Attempts)
	if res.Error != nil {
		return fmt.Errorf("claim: fail: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		q.leaseLostHook()
		return fmt.Errorf("%w: task %d", ErrLeaseLost, task.ID)
	}
	return nil
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
// Reap 把租约过期的任务放回待处理态，并把重试耗尽的转为死亡态。每个副本都可以
// 调用：UPDATE 只影响租约确已过期的行。
func (q *Queue) Reap(ctx context.Context) (reaped, dead int64, err error) {
	res := q.db.WithContext(ctx).Exec(q.reapSQL, q.job, q.maxAttempts)
	if res.Error != nil {
		return 0, 0, fmt.Errorf("claim: reap: %w", res.Error)
	}
	reaped = res.RowsAffected

	res = q.db.WithContext(ctx).Exec(q.reapDeadSQL, q.job, q.maxAttempts)
	if res.Error != nil {
		return reaped, 0, fmt.Errorf("claim: reap dead: %w", res.Error)
	}
	dead = res.RowsAffected
	if q.hooks.OnReap != nil {
		q.hooks.OnReap(q.job, reaped, dead)
	}
	return reaped, dead, nil
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
// fail_resion is different: Fail writes the bounded cause there and Succeed
// clears it, so a dead row carries its own explanation.
// DDL 返回查询所依赖的表结构，包含防止 SKIP LOCKED 退化为全表扫描的索引。表名与
// New 走同一套标识符校验。
//
// 时间列用普通 DATETIME，因此短于一秒的租约或退避会被 MySQL 取整到秒。claim 的 SQL 仍用
// NOW(3)，它与两种精度都能正确比较。created_by 与 deleted_at 是应用侧字段：claim 从不读写，
// 没有这两列的表照样能用；deleted_at 非空也不会让 Claim 跳过该行。fail_resion 不同：Fail
// 会写入有界的错误原因，Succeed 会清空，因此 dead 行本身就带着失败原因。
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
  payload     JSON         NULL COMMENT '任务载荷',
  fail_resion TEXT         NULL DEFAULT NULL COMMENT '失败原因',
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
