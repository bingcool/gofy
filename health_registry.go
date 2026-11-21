package gofy

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/support/health"
	"go.uber.org/zap"
)

// newHealthRegistry builds the HTTP process's probe registry: the checks the
// application registered, plus the database check unless
// WithDatabaseHealthCheck(false) disabled it. A registration error is logged
// rather than fatal: a bad check must not keep the process from starting, and
// the remaining checks still answer.
// newHealthRegistry 构建 HTTP 进程的探针注册表：应用注册的检查，以及数据库检查（除非
// WithDatabaseHealthCheck(false) 关闭）。注册错误只记录而不致命：一个坏检查不能挡住
// 进程启动，其余检查仍要应答。
func (a *App) newHealthRegistry(rt *core.Runtime, lg *zap.Logger) *health.Registry {
	reg := health.New(a.opts.healthOptions)

	if a.opts.databaseHealth && rt != nil && rt.DB != nil {
		if err := reg.Register(health.NewDB("database", rt.DB)); err != nil {
			lg.Warn("register database health check failed", zap.Error(err))
		}
	}

	for _, spec := range a.opts.healthChecks {
		if err := reg.Register(spec.checker, health.WithClass(spec.class)); err != nil {
			lg.Warn("register health check failed",
				zap.String("check", spec.checker.Name()),
				zap.String("class", string(spec.class)),
				zap.Error(err),
			)
		}
	}

	logHealthChecks(reg, lg)
	return reg
}

// newWorkerHealthRegistry builds the worker process's registry. The group
// readiness check is registered by the host, because only the host knows when
// every instance of every group has reported.
// newWorkerHealthRegistry 构建 worker 进程的注册表。分组就绪检查由宿主注册，因为只有
// 宿主知道每个分组的每个实例何时上报完毕。
func (a *App) newWorkerHealthRegistry(lg *zap.Logger) *health.Registry {
	reg := health.New(a.opts.healthOptions)

	for _, c := range a.opts.workerChecks {
		if err := reg.Register(c); err != nil {
			lg.Warn("register worker health check failed",
				zap.String("check", c.Name()),
				zap.Error(err),
			)
		}
	}

	logHealthChecks(reg, lg)
	return reg
}

// logHealthChecks records what this composition installed. A readiness check
// added by accident then shows up in the startup log instead of only in an
// incident.
// logHealthChecks 记录本次装配安装了哪些检查。误加成 readiness 的检查因此会出现在启动
// 日志里，而不是只在故障时才被发现。
func logHealthChecks(reg *health.Registry, lg *zap.Logger) {
	if reg == nil || lg == nil {
		return
	}
	for _, e := range reg.Entries() {
		lg.Debug("health check registered",
			zap.String("check", e.Name),
			zap.String("class", string(e.Class)),
			zap.Duration("timeout", e.Timeout),
		)
	}
}

// healthCheckJSON is one check in the diagnostic /health body.
// healthCheckJSON 是诊断 /health 响应体中的单个检查。
type healthCheckJSON struct {
	Name      string `json:"name"`
	Class     string `json:"class"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	Reason    string `json:"reason,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	CheckedAt string `json:"checked_at,omitempty"`
}

// healthBodyJSON is the /health response. It always answers 200: the endpoint
// describes state, and a load balancer never uses it to decide traffic.
// healthBodyJSON 是 /health 的响应体。它始终返回 200：该端点描述状态，负载均衡器不会
// 用它决定流量。
type healthBodyJSON struct {
	Status    string            `json:"status"`
	Ready     bool              `json:"ready"`
	CheckedAt string            `json:"checked_at,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Checks    []healthCheckJSON `json:"checks"`
}

// healthDetailHandler serves the diagnostic detail on the diagnostics listener.
// It renders readiness and diagnostic checks together, which is why an operator
// reads one endpoint instead of two.
// healthDetailHandler 在诊断监听上提供明细。它把 readiness 与 diagnostic 检查一起渲染，
// 运维因此只需读一个端点。
func (a *App) healthDetailHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Loaded per request: a reload swaps the whole registry, and a handler
		// that captured the previous one would keep reporting the retired pool.
		// 按请求读取：Reload 会整体替换注册表，捕获旧注册表的处理器会一直报告已退役的
		// 连接池。
		var snap health.Snapshot
		if reg := a.healthRegistry.Load(); reg != nil {
			snap = reg.Diagnose(r.Context())
		}

		body := healthBodyJSON{
			Status: "ok",
			Ready:  snap.Ready,
			Reason: snap.Reason,
			Checks: make([]healthCheckJSON, 0, len(snap.Results)),
		}
		if !snap.Ready {
			body.Status = "unavailable"
		}
		if !snap.CheckedAt.IsZero() {
			body.CheckedAt = snap.CheckedAt.UTC().Format(time.RFC3339Nano)
		}

		for _, res := range snap.Results {
			item := healthCheckJSON{
				Name:      res.Name,
				Class:     string(res.Class),
				Status:    string(res.Status),
				Error:     res.Error,
				Reason:    res.Reason,
				LatencyMS: res.Latency.Milliseconds(),
			}
			if !res.CheckedAt.IsZero() {
				item.CheckedAt = res.CheckedAt.UTC().Format(time.RFC3339Nano)
			}
			body.Checks = append(body.Checks, item)
		}

		writeHealthJSON(w, http.StatusOK, body)
	})
}

func writeHealthJSON(w http.ResponseWriter, status int, body any) {
	payload, err := json.Marshal(body)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}
