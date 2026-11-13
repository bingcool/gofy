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

// newOrderScanWorker scans pending orders on an interval. The count is a
// placeholder: support/claim (T3-7) will replace it with a row-level claim, so
// the scan can run on every replica without processing a row twice.
// newOrderScanWorker 按间隔扫描待处理订单。当前只统计数量，占位到 support/claim
// （T3-7）落地后改成按行认领，届时每个副本都能安全地扫同一张表。
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
