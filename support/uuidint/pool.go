package uuidint

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// idPool is the local prefetch buffer, ported from the PHP UuidManager channel
// pool: a ticker pulls whole batches from Redis and Get/GetBatch prefer the
// buffered IDs.
//
// Unlike PHP, where the channel is shared statically, the pool belongs to one
// Generator, so instances with different business keys never share a batch.
// idPool 是本地预取缓冲，移植自 PHP UuidManager 的 Channel 池：ticker 定时
// 从 Redis 整批取号，Get/GetBatch 优先消费缓冲里的号。
//
// 与 PHP 不同，池属于单个 Generator（PHP 的 Channel 是静态共享的），不同
// 业务 key 的实例不会混用同一批号。
type idPool struct {
	ids      chan int64    // 预取好的号，FIFO 消费
	size     int           // ids requested per batch, also the buffer capacity; 每批申请数量，同时是缓冲容量
	interval time.Duration // refill interval of the background ticker; 定时补货周期

	mu        sync.Mutex
	started   bool               // StartPool 是否已执行
	stopped   bool               // StopPool 是否已执行
	cancel    context.CancelFunc // 取消后台 ticker
	done      chan struct{}      // 后台 goroutine 退出后关闭
	closedIDs sync.Once          // 关闭 ids 只执行一次，超时后重试可以接着关
	lastDrain time.Time          // last consume time, used to tell whether the pool is backed up; 最近一次消费时间，用于判断池是否积压
}

func newIDPool(size int, interval time.Duration) *idPool {
	return &idPool{
		ids:      make(chan int64, size),
		size:     size,
		interval: interval,
		done:     make(chan struct{}),
	}
}

// StartPool enables the prefetch pool: it first requests one batch
// synchronously so IDs are available as soon as it returns, then starts a
// ticker that refills the buffer. It is a no-op when the pool was not
// configured through WithPool, and repeated calls are ignored. The pool cannot
// be restarted after StopPool; build a new Generator to bring it back.
// StartPool 启用预取池：先同步申请一批，保证返回后立即可取号；随后启动
// ticker 定时补货。未通过 WithPool 配置池时是空操作；重复调用会被忽略。
// StopPool 之后不支持重启，需要恢复池时重新 New 一个 Generator。
func (g *Generator) StartPool(ctx context.Context) error {
	if g == nil || g.pool == nil {
		return nil
	}
	p := g.pool

	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return nil
	}
	p.started = true
	if ctx == nil {
		ctx = context.Background()
	}
	poolCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.lastDrain = time.Now()
	p.mu.Unlock()

	g.refill(poolCtx)

	go func() {
		defer close(p.done)
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-poolCtx.Done():
				return
			case <-ticker.C:
				g.refill(poolCtx)
			}
		}
	}()
	return nil
}

// StopPool stops the ticker and closes the buffer; it is safe to call
// repeatedly and without StartPool. It waits without a deadline, so a caller
// that cannot afford to wait must use StopPoolContext. Afterwards Get/GetBatch
// go straight to Redis and unused buffered IDs are discarded.
// StopPool 停止 ticker 并关闭缓冲，可重复调用；未 StartPool 时也安全。它没有截止
// 时间，等不起的调用方应改用 StopPoolContext。返回后 Get/GetBatch 直接访问
// Redis，缓冲里剩余未使用的号会被丢弃。
func (g *Generator) StopPool() {
	_ = g.StopPoolContext(context.Background())
}

// StopPoolContext is StopPool with a budget. Cancelling the ticker does not
// interrupt the Redis call it is already inside — the pool context carries no
// deadline, and go-redis only shortens a command by the deadline in ctx — so
// that call has to run into read_timeout before the goroutine can return. This
// version gives up at the deadline instead of holding the caller for it, and
// reports ctx.Err().
//
// Giving up leaves a prefetch goroutine behind and, deliberately, the buffer
// open: closing it here would panic that goroutine's next send. A later Get
// drains whatever is left and goes to Redis, and a repeated call can still
// finish the job once the ticker exits.
// StopPoolContext 是带预算的 StopPool。取消 ticker 不会打断它已经进入的那次 Redis
// 调用——池的 ctx 没有 deadline，而 go-redis 只会按 ctx 里的 deadline 缩短命令——
// 那条调用必须撞上 read_timeout 才会返回。所以这里在截止时间放弃等待并回报
// ctx.Err()，而不是把调用方一起挂住。
//
// 放弃会留下一个预取 goroutine，并且故意不关闭缓冲：此时关闭会让被放弃的
// goroutine 下一次发送 panic。之后的 Get 会取空剩余号并直接访问 Redis；ticker 真
// 退出后再调一次也能把收尾做完。
func (g *Generator) StopPoolContext(ctx context.Context) error {
	if g == nil || g.pool == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p := g.pool

	p.mu.Lock()
	if !p.started {
		// Never started: done is still open, so waiting on it would block
		// forever, and there is nothing to stop either.
		// 从未启动：done 仍然打开，等它就是永远等，而且也没有东西要停。
		p.mu.Unlock()
		return nil
	}
	first := !p.stopped
	p.stopped = true
	cancel := p.cancel
	p.mu.Unlock()

	if first && cancel != nil {
		cancel()
	}
	select {
	case <-p.done:
		p.closedIDs.Do(func() { close(p.ids) })
		return nil
	case <-ctx.Done():
		return fmt.Errorf("uuidint: pool ticker did not stop: %w", ctx.Err())
	}
}

// drainPool takes up to n IDs from the buffer without touching Redis; when the
// buffer is already closed it returns whatever was collected.
// drainPool 从缓冲取至多 n 个号，不访问 Redis；缓冲已关闭时返回已取到的
// 部分。
func (g *Generator) drainPool(n int) []int64 {
	if g.pool == nil {
		return nil
	}
	ids := make([]int64, 0, n)
	for len(ids) < n {
		select {
		case id, ok := <-g.pool.ids:
			if !ok {
				return ids
			}
			ids = append(ids, id)
		default:
			if len(ids) > 0 {
				g.markDrained()
			}
			return ids
		}
	}
	g.markDrained()
	return ids
}

// markDrained records the time of this consumption; refill uses it to decide
// whether the pool has been idle for too long.
// markDrained 记录本次消费时间，refill 用它判断池是否长时间无人使用。
func (g *Generator) markDrained() {
	g.pool.mu.Lock()
	g.pool.lastDrain = time.Now()
	g.pool.mu.Unlock()
}

// refill requests one batch of IDs from Redis and puts it into the buffer:
//   - when the buffer still holds IDs and nothing was consumed for more than 3
//     intervals the backlog is stale, so drain it before refilling (matching PHP
//     UuidManager);
//   - skip the round without touching Redis when the pool is already full;
//   - on failure generate has already retried and fired the callback, so return;
//   - IDs discarded because the pool filled up mid-push only leave gaps in the
//     counter and never hurt uniqueness.
//
// refill 向 Redis 申请一批号并放进缓冲：
//   - 缓冲里还有号、且超过 3 个周期没人消费，说明积压过久，先清空再补，
//     避免旧号长期占位（与 PHP UuidManager 一致）；
//   - 池已满则跳过本轮，完全不访问 Redis；
//   - 申请失败时 generate 已经完成重试与告警回调，这里直接返回；
//   - 推送过程中池被填满则丢弃剩余号：只会在计数上留下空洞，不影响唯一性。
func (g *Generator) refill(ctx context.Context) {
	p := g.pool

	p.mu.Lock()
	if len(p.ids) > 0 && time.Since(p.lastDrain) > 3*p.interval {
		for len(p.ids) > 0 {
			<-p.ids
		}
	}
	p.mu.Unlock()

	if len(p.ids) >= cap(p.ids) {
		return
	}
	maxID, err := g.generate(ctx, p.size)
	if err != nil {
		return
	}
	for _, id := range expandRange(maxID, p.size) {
		select {
		case p.ids <- id:
		default:
			return
		}
	}
}
