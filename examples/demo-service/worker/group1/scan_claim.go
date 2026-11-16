package group1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	gofyclaim "github.com/bingcool/gofy/support/claim"
	gofyworker "github.com/bingcool/gofy/support/worker"
)

// This file is the database-queue counterpart of scan.go: where that loop only
// counts rows and would count them twice on two replicas, this pair hands work
// out with row-level ownership.
//
// Both halves live here on purpose — the enqueue statement is the other half of
// the claim contract, and reading them apart is how the payload shape and the
// next_run_at semantics get out of sync. In a real service the INSERT belongs to
// the business transaction that creates the work (create the order and the job
// in one commit); the timer below exists so the sample runs on its own.
//
// 这个文件是 scan.go 的数据库队列版本：那边只统计行数，两个副本会各统计一遍；这里用行级
// 归属把工作分出去。
//
// 两半放在同一个文件里是有意的——入队语句是认领契约的另一半，分开读正是 payload 结构与
// next_run_at 语义走样的原因。真实服务里这条 INSERT 属于创建工作的那个业务事务（建订单与
// 建任务同一个提交）；下面的定时器只是让样例能自己跑起来。

// ClaimScanConfig is the claim sample's own knobs: how often the producer
// enqueues, how often the consumers claim, how large a batch is and how long a
// claim stays valid. The numbers live next to the loops they drive, like
// ScanWorkerConfig — nothing outside this file reads them.
// ClaimScanConfig 是认领样例自己的参数：生产者多久入队、消费者多久认领、每批多大、认领
// 有效期多长。数字和它驱动的循环在同一个文件里，与 ScanWorkerConfig 一致，包外没人读。
type ClaimScanConfig struct {
	// Concurrency is how many claim instances run in this process. Each one
	// claims under its own owner string, so the task table shows which instance
	// holds a row — that is what makes the sample worth running with 2.
	// Concurrency 是进程内的认领实例数。每个实例用自己的 owner 声明，任务表因此能看到一行
	// 被哪个实例持有——这正是样例值得用 2 个实例跑的原因。
	Concurrency int
	// IntervalMS is the pause between two claim rounds.
	// IntervalMS 是两轮认领之间的间隔。
	IntervalMS uint32
	// EnqueueIntervalMS is the pause between two enqueue rounds.
	// EnqueueIntervalMS 是两轮入队之间的间隔。
	EnqueueIntervalMS uint32
	// Batch is the most rows one claim round takes.
	// Batch 是一轮认领最多取回的行数。
	Batch int
	// Lease is how long a claim stays valid. It has to exceed the slowest legal
	// handler: claim does not renew it, and an expired lease hands the row to
	// somebody else.
	// Lease 是认领有效期。它必须大于最慢的合法 handler：claim 不续期，租约一过这行就归别人。
	Lease time.Duration
	// MaxAttempts caps how many claims one row gets before it is parked as dead.
	// MaxAttempts 限制一行在被置为死亡态之前可被认领的次数。
	MaxAttempts int
	// Backoff is the first retry delay; it doubles per attempt up to
	// gofyclaim.DefaultMaxBackoff. The sample uses two seconds so the retry
	// ladder fits on one screen; a real queue should keep claim.DefaultBackoff
	// (ten seconds) or more, because the delay is the window in which a
	// downstream outage can heal.
	// Backoff 是首次重试延迟，之后每次翻倍，上限是 gofyclaim.DefaultMaxBackoff。样例用两秒，
	// 为的是让重试阶梯在一屏日志里能看完；真实队列应保留 claim.DefaultBackoff（十秒）或更久，
	// 因为这段延迟正是下游故障自愈的窗口。
	Backoff time.Duration
	// HandlerTimeout bounds one handler call. Without it a stuck query keeps the
	// row claimed until the lease runs out and another instance repeats the work.
	// HandlerTimeout 限制单次 handler 的耗时。没有它，卡住的查询会一直占着这行，直到租约
	// 过期、另一个实例把同样的活再干一遍。
	HandlerTimeout time.Duration
	// Table is the task table declared by migrations/0005_job_task.sql.
	// Table 是 migrations/0005_job_task.sql 建立的任务表。
	Table string
	// Job tags the rows this sample owns; one table can host several jobs.
	// Job 标记这个样例自己的行；一张表可以承载多个 job。
	Job string
}

// withDefaults fills every zero field, so a caller can pass a partial config and
// still get a working worker.
// withDefaults 补齐所有零值字段，调用方传一份局部配置也能得到可用的 worker。
func (c ClaimScanConfig) withDefaults() ClaimScanConfig {
	if c.Concurrency < 1 {
		c.Concurrency = 1
	}
	if c.IntervalMS == 0 {
		c.IntervalMS = 2000
	}
	if c.EnqueueIntervalMS == 0 {
		c.EnqueueIntervalMS = 10000
	}
	if c.Batch <= 0 {
		c.Batch = 3
	}
	if c.Lease <= 0 {
		c.Lease = gofyclaim.DefaultLease
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = gofyclaim.DefaultMaxAttempts
	}
	if c.Backoff <= 0 {
		c.Backoff = gofyclaim.DefaultBackoff
	}
	if c.HandlerTimeout <= 0 {
		c.HandlerTimeout = 5 * time.Second
	}
	if c.Table == "" {
		c.Table = "job_task"
	}
	if c.Job == "" {
		c.Job = "order.settle"
	}
	return c
}

// claimScanDefaults is what group1 runs with: a batch of three every three
// seconds from two instances, a claim valid for thirty seconds and three
// attempts before a row is parked. The lease is deliberately an order of magnitude above the
// handler: the sample's work is one query, and the margin is what the comment on
// Lease asks a real service to keep. The two-second backoff is the one value
// chosen for the demo rather than for production — see the field comment.
// claimScanDefaults 是 group1 的运行参数：两个实例每三秒认领一批三行，认领有效期三十秒，
// 一行三次不过就置为死亡态。租约刻意比 handler 高一个数量级：样例的活只有一次查询，而这个
// 余量正是 Lease 注释要求真实服务保持的。两秒的重试延迟是唯一为演示而选的值——见字段注释。
var claimScanDefaults = ClaimScanConfig{
	Concurrency:       2,
	IntervalMS:        3000,
	EnqueueIntervalMS: 10000,
	Batch:             3,
	Lease:             30 * time.Second,
	MaxAttempts:       3,
	Backoff:           2 * time.Second,
	HandlerTimeout:    5 * time.Second,
	Table:             "job_task",
	Job:               "order.settle",
}

// claimJobPayload is what the producer writes into the payload column and what
// the consumer reads back. Fail is the sample's way of exercising the retry and
// dead paths without breaking anything real.
// claimJobPayload 是生产者写进 payload 列、消费者读回来的结构。Fail 是样例用来跑通重试与
// 死亡态两条路径的开关，不破坏任何真实数据。
type claimJobPayload struct {
	UserID uint64 `json:"user_id"`
	Note   string `json:"note"`
	Fail   bool   `json:"fail"`
}

// newClaimScanProducer enqueues a small batch of settlement jobs every interval,
// including one with a due time in the future and, every third round, one that
// is meant to fail.
//
// It writes the rows the way a business path would — plain INSERTs in one
// transaction, no queue API — because that is the contract: a row with status 0
// and a due next_run_at is the entire enqueue.
// newClaimScanProducer 每个间隔入队一小批结算任务：其中一条的到期时间在未来，每三轮再加
// 一条注定失败的。
//
// 它按业务路径的方式写行——一个事务里的几条普通 INSERT，没有队列 API——因为契约就是如此：
// 一行 status 0 且 next_run_at 到期的记录，就是全部入队动作。
func newClaimScanProducer(cfg ClaimScanConfig) gofyworker.Worker {
	cfg = cfg.withDefaults()
	return gofyworker.Func("order-settle-producer", 1, func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		if !ok || rt.DB == nil {
			return errors.New("order-settle-producer: runtime database is required")
		}
		id, _ := gofyworker.IdentityFrom(ctx)
		lg := claimLogger(rt.Logger, id)
		if err := ensureClaimTable(ctx, rt.DB, cfg.Table); err != nil {
			return fmt.Errorf("order-settle-producer: %w", err)
		}

		lg.Info("claim producer started",
			zap.String("job", cfg.Job),
			zap.String("table", cfg.Table),
			zap.Uint32("interval_ms", cfg.EnqueueIntervalMS),
		)
		defer lg.Info("claim producer stopped")

		round := 0
		return gofyworker.Ticker(ctx, cfg.EnqueueIntervalMS, func(ctx context.Context) error {
			round++
			// The drain context lets an INSERT that already reached the server
			// commit during shutdown; the timeout keeps a stuck connection from
			// holding the tick.
			// drain context 让已经到达服务端的 INSERT 在停机时提交完；超时则避免卡住的连接
			// 拖住这一轮。
			enqueueCtx, cancel := context.WithTimeout(gofyworker.DrainContext(ctx), 3*time.Second)
			defer cancel()

			// Five ready rows plus one that is not due yet, against a batch of
			// three: one instance takes three, the other takes the remaining
			// two, so the log shows both owners and a real batch in the same
			// round. With the batch equal to the row count the faster instance
			// would take everything and the second would look idle.
			// 五条立即可认领，外加一条尚未到期；批大小是三：一个实例拿三条，另一个拿剩下的两条，
			// 同一轮日志里因此既有真实批次也有两个 owner。批大小若等于行数，先到的实例会全部拿走，
			// 另一个看起来就像没干活。
			rows := []claimJob{
				{userID: 1, note: "immediate"},
				{userID: 2, note: "immediate"},
				{userID: 3, note: "immediate"},
				{userID: 1, note: "immediate"},
				{userID: 2, note: "immediate"},
				// One row is not due yet: next_run_at is what makes the claim
				// query skip it until the delay has passed.
				// 一条尚未到期：认领查询靠 next_run_at 跳过它，直到延迟走完。
				{userID: 1, note: "visible in 15s", after: 15 * time.Second},
			}
			if round%3 == 0 {
				rows = append(rows, claimJob{userID: 3, note: "always fails", fail: true})
			}
			// One transaction per round. A claim that ticks mid-round would
			// otherwise see half a batch, and in a real service these INSERTs
			// share the transaction that creates the work anyway — the row and
			// the job commit together or not at all.
			// 每轮一个事务。否则在插入过程中 tick 的认领会看到半个批次；真实服务里这些 INSERT
			// 本来就与创建工作的那个事务同生共死——业务行与任务行要么一起提交，要么都不提交。
			if err := rt.DB.WithContext(enqueueCtx).Transaction(func(tx *gorm.DB) error {
				for _, row := range rows {
					if err := enqueueClaimJob(enqueueCtx, tx, cfg, row); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return err
			}
			lg.Info("claim jobs enqueued", zap.String("job", cfg.Job), zap.Int("count", len(rows)))
			return nil
		})
	})
}

// claimJob is one row to enqueue.
// claimJob 是要入队的一行。
type claimJob struct {
	userID uint64
	note   string
	fail   bool
	after  time.Duration
}

// enqueueClaimJob writes one pending row.
//
// next_run_at is computed by the database, not by this process: the claim query
// compares it with NOW(3), and mixing two clocks would make a delayed job early
// on one node and late on another — the same reason the ledger's lease is
// database time.
// enqueueClaimJob 写入一条待处理记录。
//
// next_run_at 由数据库计算，不由本进程算：认领查询拿它与 NOW(3) 比较，两个时钟混用会让
// 延迟任务在某个节点上提前、在另一个节点上迟到——与台账租约用数据库时间是同一个理由。
func enqueueClaimJob(ctx context.Context, db *gorm.DB, cfg ClaimScanConfig, job claimJob) error {
	payload, err := json.Marshal(claimJobPayload{
		UserID: job.userID,
		Note:   job.note,
		Fail:   job.fail,
	})
	if err != nil {
		return fmt.Errorf("encode claim payload: %w", err)
	}
	row := map[string]any{
		// The Go option is Job, the column is job_type.
		// Go 侧选项是 Job，列名是 job_type。
		"job_type": cfg.Job,
		"status":   gofyclaim.StatusPending,
		"payload":  string(payload),
	}
	if job.after > 0 {
		row["next_run_at"] = gorm.Expr("DATE_ADD(NOW(3), INTERVAL ? MICROSECOND)", job.after.Microseconds())
	}
	// Table() quotes the name and Create() binds every value, so the statement
	// stays free of string building even though it is a sample.
	// Table() 负责引用表名，Create() 绑定所有值，因此即使是样例，语句里也没有拼字符串。
	if err := db.WithContext(ctx).Table(cfg.Table).Create(row).Error; err != nil {
		return fmt.Errorf("enqueue claim job: %w", err)
	}
	return nil
}

// newClaimScanConsumer claims due rows and settles them. This is the worker that
// replaces a naive "SELECT ... WHERE status = pending" scan: the claim happens in
// one transaction with FOR UPDATE SKIP LOCKED, so every replica can run the same
// loop and no row is handled twice.
// newClaimScanConsumer 认领到期行并结算。它就是用来替换"SELECT ... WHERE status =
// pending"那种裸扫描的：认领在一个 FOR UPDATE SKIP LOCKED 的事务里完成，因此每个副本都能
// 跑同一个循环，而不会有任何一行被处理两次。
func newClaimScanConsumer(cfg ClaimScanConfig) gofyworker.Worker {
	cfg = cfg.withDefaults()
	return gofyworker.Func("order-settle-consumer", uint8(cfg.Concurrency), func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		if !ok || rt.DB == nil {
			return errors.New("order-settle-consumer: runtime database is required")
		}
		id, _ := gofyworker.IdentityFrom(ctx)
		lg := claimLogger(rt.Logger, id)
		if err := ensureClaimTable(ctx, rt.DB, cfg.Table); err != nil {
			return fmt.Errorf("order-settle-consumer: %w", err)
		}

		// The queue is built inside Run, not in NewGroup: it holds this
		// generation's pool, and the runner restarts Run with a fresh one after
		// a reload.
		// 队列在 Run 内构建，而不是在 NewGroup：它持有这一代的连接池，热更新后 runner 会用
		// 新一代重新进入 Run。
		queue, err := gofyclaim.New(rt.DB, gofyclaim.Config{
			Table:       cfg.Table,
			Job:         cfg.Job,
			Batch:       cfg.Batch,
			Lease:       cfg.Lease,
			MaxAttempts: cfg.MaxAttempts,
			Backoff:     cfg.Backoff,
			// Hooks run on the goroutine that lost the lease, which is where the
			// log line belongs.
			// Hooks 在丢失租约的那个 goroutine 上执行，日志正好该写在那里。
			Hooks: gofyclaim.Hooks{
				OnLeaseLost: func(job string) {
					lg.Warn("claim lease lost", zap.String("job", job))
				},
			},
		})
		if err != nil {
			return err
		}

		// Owner identifies this instance, not this claim: the same instance
		// re-claiming a row after a reap keeps the same owner, which is why the
		// fencing has to compare attempts as well.
		// owner 标识的是这个实例，不是这次认领：同一实例在 Reap 之后再次认领同一行，owner
		// 不变，因此围栏必须同时比较 attempts。
		owner := id.Owner()
		lg = lg.With(zap.String("owner", owner))
		lg.Info("claim consumer started",
			zap.String("job", cfg.Job),
			zap.String("table", cfg.Table),
			zap.Int("batch", cfg.Batch),
			zap.Duration("lease", cfg.Lease),
			zap.Duration("backoff", cfg.Backoff),
			zap.Int("max_attempts", cfg.MaxAttempts),
		)
		defer lg.Info("claim consumer stopped")

		return gofyworker.Ticker(ctx, cfg.IntervalMS, func(ctx context.Context) error {
			drain := gofyworker.DrainContext(ctx)

			// Reap before claiming. A row left behind by a crashed instance is
			// invisible to Claim until its lease expires and somebody collects
			// it; doing it here means every replica repairs the queue and no
			// separate janitor job is needed. The statement only touches rows
			// whose lease really expired, so two instances reaping at once is
			// harmless.
			// 先 Reap 再认领。崩溃实例留下的行在租约过期并被回收之前对 Claim 不可见；放在这里
			// 意味着每个副本都在修队列，不需要单独的清理任务。语句只碰租约确已过期的行，因此
			// 两个实例同时回收也无害。
			reaped, dead, reapErr := queue.Reap(drain)
			if reapErr != nil {
				lg.Warn("claim reap failed", zap.Error(reapErr))
			} else if reaped+dead > 0 {
				lg.Warn("claim reaped expired leases",
					zap.Int64("requeued", reaped),
					zap.Int64("dead", dead),
				)
			}

			// n = 0 means "use Config.Batch". An empty result is normal: the
			// loop is a poll, not a subscription, so the next tick simply tries
			// again — there is nothing to back off from.
			// n = 0 表示用 Config.Batch。空结果是常态：这个循环是轮询而不是订阅，下一个 tick
			// 再试即可，没有什么需要退避。
			tasks, err := queue.Claim(drain, owner, 0)
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				return nil
			}
			lg.Info("claim batch", zap.Int("claimed", len(tasks)), zap.Int("batch", cfg.Batch))

			// Every task in the batch is attempted even if one of them fails to
			// write its outcome: a failed bookkeeping write only costs this row a
			// lease expiry, and Reap will requeue it. The errors are joined so the
			// tick still reports them.
			// 即使某一条的结果写不进去，批次里其余任务照常处理：收尾写失败只让这一行多等一个
			// 租约周期，Reap 会把它放回待处理。错误合并上抛，这个 tick 仍然会报告。
			var errs []error
			for _, task := range tasks {
				if err := settleClaimedTask(drain, rt.DB, lg, queue, cfg, task); err != nil {
					errs = append(errs, err)
				}
			}
			return errors.Join(errs...)
		})
	})
}

// settleClaimedTask runs one handler and records its outcome: Succeed on
// success, Fail on failure — the latter also moves the row to dead once the
// attempt cap is reached, which is why the sample never calls a "dead" method
// itself.
// settleClaimedTask 执行一个 handler 并记录结果：成功用 Succeed，失败用 Fail——后者在尝试
// 次数用尽时还会把行置为死亡态，因此样例自己从不调用"置死"方法。
func settleClaimedTask(
	ctx context.Context,
	db *gorm.DB,
	lg *zap.Logger,
	queue *gofyclaim.Queue,
	cfg ClaimScanConfig,
	task gofyclaim.Task,
) error {
	started := time.Now()
	handleCtx, cancel := context.WithTimeout(ctx, cfg.HandlerTimeout)
	summary, handleErr := settleOrderJob(handleCtx, db, task)
	cancel()
	elapsed := time.Since(started)

	if handleErr != nil {
		failErr := queue.Fail(ctx, task, handleErr)
		if errors.Is(failErr, gofyclaim.ErrLeaseLost) {
			// The row was taken over while this handler ran — its lease expired
			// and another instance claimed it. Whatever this handler did may have
			// happened twice already, so nothing is retried here.
			// 这一行在本 handler 执行期间被接管了——租约过期，别的实例认领了它。这个 handler
			// 做过的事可能已经发生了两遍，因此这里什么都不重试。
			lg.Warn("claim job lost its lease; not retrying",
				zap.Uint64("task_id", task.ID),
				zap.Int("attempt", task.Attempts),
				zap.Duration("duration", elapsed),
			)
			return nil
		}
		if failErr != nil {
			return failErr
		}
		if task.Attempts >= cfg.MaxAttempts {
			lg.Error("claim job exhausted its attempts; parked as dead",
				zap.Uint64("task_id", task.ID),
				zap.Int("attempts", task.Attempts),
				zap.Duration("duration", elapsed),
				zap.Error(handleErr),
			)
			return nil
		}
		lg.Warn("claim job failed; retry scheduled",
			zap.Uint64("task_id", task.ID),
			zap.Int("attempt", task.Attempts),
			zap.Duration("duration", elapsed),
			zap.Error(handleErr),
		)
		return nil
	}

	if err := queue.Succeed(ctx, task); err != nil {
		if errors.Is(err, gofyclaim.ErrLeaseLost) {
			lg.Warn("claim job lost its lease before completion",
				zap.Uint64("task_id", task.ID),
				zap.Int("attempt", task.Attempts),
			)
			return nil
		}
		// The work is done but the row still says claimed. Returning the error
		// leaves the repair to Reap: the lease expires, the row goes back to
		// pending, and the handler runs again — which is exactly why a handler
		// has to be idempotent.
		// 活干完了，但这一行还写着 claimed。把错误上抛，修复交给 Reap：租约过期、行回到
		// pending、handler 再跑一遍——这正是 handler 必须幂等的原因。
		return err
	}
	lg.Info("claim job settled",
		zap.Uint64("task_id", task.ID),
		zap.Int("attempt", task.Attempts),
		zap.Int64("orders", summary.Orders),
		zap.Int64("amount", summary.Amount),
		zap.Duration("duration", elapsed),
	)
	return nil
}

// orderSettlement is the business result of one task: a user's orders rolled up.
// A real handler would write this somewhere, and that write would have to be
// idempotent — the same task runs again whenever its lease expires mid-flight.
// orderSettlement 是一个任务的业务结果：某个用户订单的汇总。真实的 handler 会把它写到某个
// 地方，而那次写必须幂等——租约在执行中过期时，同一个任务会再跑一遍。
type orderSettlement struct {
	Orders int64
	Amount int64
}

// settleOrderJob is the business half: it reads the payload, does the work and
// reports the outcome. It never touches the queue — Succeed and Fail belong to
// the caller, which is what keeps the fencing pair out of business code.
// settleOrderJob 是业务的一半：读 payload、干活、报告结果。它从不碰队列——Succeed 与 Fail
// 归调用方，围栏字段因此不会流进业务代码。
func settleOrderJob(ctx context.Context, db *gorm.DB, task gofyclaim.Task) (orderSettlement, error) {
	var payload claimJobPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		// A payload that cannot be decoded never becomes decodable: let it burn
		// its attempts and land in dead, which is where a human looks.
		// 解不开的 payload 不会自己变好：让它耗完重试落进死信，那才是有人会去看的地方。
		return orderSettlement{}, fmt.Errorf("decode payload of task %d: %w", task.ID, err)
	}
	if payload.Fail {
		return orderSettlement{}, fmt.Errorf("simulated settlement failure (task %d, %q)", task.ID, payload.Note)
	}

	// The work itself: aggregate the user's orders. It is a read, so it is
	// naturally idempotent; a write would need an upsert or a unique key to earn
	// the same property.
	// 活本身：汇总该用户的订单。因为是读，它天然幂等；写操作要靠 upsert 或唯一键才能拿到
	// 同样的性质。
	var summary orderSettlement
	if err := db.WithContext(ctx).Table("orders").
		Select("COUNT(*) AS orders, COALESCE(SUM(amount), 0) AS amount").
		Where("user_id = ?", payload.UserID).
		Scan(&summary).Error; err != nil {
		return orderSettlement{}, fmt.Errorf("summarise orders of user %d: %w", payload.UserID, err)
	}
	return summary, nil
}

// ensureClaimTable turns "the migration did not run" into a startup error the
// log shows verbatim, instead of one warning per tick that reads like a database
// hiccup. The runner restarts a failed worker with backoff, so applying the
// migration is enough to recover without touching the process.
// ensureClaimTable 把"迁移没跑"变成日志里一眼能读懂的启动错误，而不是每 tick 一条看起来像
// 数据库抖动的警告。runner 会带退避地重启失败的 worker，因此补完迁移就会自愈，不必动进程。
func ensureClaimTable(ctx context.Context, db *gorm.DB, table string) error {
	if db.WithContext(ctx).Migrator().HasTable(table) {
		return nil
	}
	return fmt.Errorf("claim table %q is missing: apply migrations/0005_job_task.sql", table)
}

// claimLogger scopes the runtime logger to one instance, and stays a no-op when
// the runtime has none — a worker should not panic over a missing logger.
// claimLogger 把 Runtime 的 logger 收窄到某个实例；Runtime 没有 logger 时保持 no-op——
// worker 不该因为少一个 logger 而 panic。
func claimLogger(base *zap.Logger, id gofyworker.Identity) *zap.Logger {
	if base == nil {
		return zap.NewNop()
	}
	return base.With(zap.String("worker", id.String()))
}
