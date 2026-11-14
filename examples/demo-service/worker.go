package main

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/bingcool/gofy/support/worker"
	"github.com/yourorg/demo-service/app/config"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
)

// newOrderWorkerGroup builds the group started by
// `demo-service worker --group=order`.
//
// The workers call the same repository-level data as the HTTP handlers instead
// of reimplementing queries, and they never touch support.Router: a worker
// process has no HTTP stack.
// newOrderWorkerGroup 构造 `demo-service worker --group=order` 启动的分组。
//
// worker 复用与 HTTP 处理器相同的数据访问，而不是另写一套查询；它们也不碰
// support.Router：worker 进程里没有 HTTP 栈。
func newOrderWorkerGroup(scan config.ScanWorkerConfig) (*worker.Group, error) {
	return worker.NewGroup("order", newOrderScanWorker(scan), newOrderHeartbeatWorker())
}

// newOrderScanWorker scans pending orders on an interval. Demo's orders table
// has no claim columns (status is a business value, not a task state), so this
// only counts; a real queue declares a job table with claim.DDL and calls
// claim.Queue.Claim, which is what makes several replicas safe.
// newOrderScanWorker 按间隔扫描待处理订单。demo 的 orders 表没有认领所需的状态列
// （status 是业务值，不是任务状态），因此这里只统计；真实的队列用 claim.DDL 建
// 任务表，再调 claim.Queue.Claim，多副本才安全。
func newOrderScanWorker(cfg config.ScanWorkerConfig) worker.Worker {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return worker.Func("order-scan", func(ctx context.Context) error {
		rt, ok := worker.RuntimeFrom(ctx)
		if !ok || rt.DB == nil {
			return errors.New("order-scan: runtime database is required")
		}
		id, _ := worker.IdentityFrom(ctx)
		lg := rt.Logger.With(zap.String("worker", id.String()))

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		lg.Info("order scan started", zap.Duration("interval", interval))
		for {
			select {
			case <-ctx.Done():
				lg.Info("order scan stopped")
				return nil
			case <-ticker.C:
				// The count uses the drain context: a shutdown must not abort a
				// query that is already talking to the database.
				// 计数使用 drain context：停机不应该中断已经在与数据库交互的查询。
				queryCtx, cancel := context.WithTimeout(worker.DrainContext(ctx), 3*time.Second)
				var pending int64
				err := rt.DB.WithContext(queryCtx).
					Table("orders").
					Where("status = ?", string(orderenum.OrderStatusPending)).
					Count(&pending).Error
				cancel()
				if err != nil {
					lg.Warn("order scan failed", zap.Error(err))
					continue
				}
				lg.Info("order scan tick", zap.Int64("pending", pending))
			}
		}
	})
}

// newOrderHeartbeatWorker proves a group can host tasks that need no database;
// it also gives operators a per-instance liveness line to grep for.
// newOrderHeartbeatWorker 说明分组里可以放不依赖数据库的任务，同时给运维留下一行
// 可 grep 的实例存活日志。
func newOrderHeartbeatWorker() worker.Worker {
	return worker.Func("order-heartbeat", func(ctx context.Context) error {
		rt, _ := worker.RuntimeFrom(ctx)
		id, _ := worker.IdentityFrom(ctx)

		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if rt != nil {
					rt.Logger.Info("worker heartbeat",
						zap.String("worker", id.String()),
						zap.String("host", id.Host),
					)
				}
			}
		}
	})
}
