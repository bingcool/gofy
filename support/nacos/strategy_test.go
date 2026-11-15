package nacos

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestParseChangeStrategy(t *testing.T) {
	cases := []struct {
		in      string
		want    ChangeStrategy
		wantErr bool
	}{
		{in: "reload", want: StrategyReload},
		{in: "restart", want: StrategyRestart},
		{in: "exit", want: StrategyExit},
		{in: " RESTART ", want: StrategyRestart},
		{in: "EXIT", want: StrategyExit},
		{in: "", wantErr: true},
		{in: "exitt", wantErr: true},
		{in: "restart-now", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseChangeStrategy(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseChangeStrategy(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ParseChangeStrategy(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestLoadConfigChangeStrategy(t *testing.T) {
	const plainApp = "nacos:\n  service_config:\n    data_id: gofy-service\n    group: GO_GROUP\n"
	cases := []struct {
		name    string
		appYAML string
		env     string
		want    ChangeStrategy
		wantErr string
	}{
		{name: "default is reload", appYAML: plainApp, want: StrategyReload},
		{name: "from application file", appYAML: plainApp + "  config_change_strategy: exit\n", want: StrategyExit},
		{name: "from environment", appYAML: plainApp, env: "restart", want: StrategyRestart},
		{name: "environment wins over file for strategy", appYAML: plainApp + "  config_change_strategy: exit\n", env: "restart", want: StrategyRestart},
		{name: "case insensitive", appYAML: plainApp + "  config_change_strategy: RESTART\n", want: StrategyRestart},
		{name: "invalid file value", appYAML: plainApp + "  config_change_strategy: exitt\n", wantErr: "invalid config_change_strategy"},
		{name: "invalid environment value", appYAML: plainApp, env: "exitt", wantErr: "invalid config_change_strategy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearNacosEnv(t)
			if tc.env != "" {
				t.Setenv("NACOS_CONFIG_CHANGE_STRATEGY", tc.env)
			}
			dir := t.TempDir()
			nacosPath := writeTestFile(t, dir, "nacos.yaml", "nacos:\n  host: 127.0.0.1\n  namespace: dev\n")
			appPath := writeTestFile(t, dir, "application.yaml", tc.appYAML)

			cfg, err := Load(nacosPath, appPath)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.ChangeStrategy != tc.want {
				t.Fatalf("ChangeStrategy = %q, want %q", cfg.ChangeStrategy, tc.want)
			}
		})
	}
}

func TestWatcherStrategyDispatch(t *testing.T) {
	result := Result{File: "config.yaml", Changed: true}
	for _, tc := range []struct {
		name     string
		strategy ChangeStrategy
		want     string
	}{
		{name: "reload calls the change handler", strategy: StrategyReload, want: "reload"},
		{name: "restart re-executes", strategy: StrategyRestart, want: "restart"},
		{name: "exit terminates", strategy: StrategyExit, want: "exit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := ""
			w := &Watcher{
				logger:   zap.NewNop(),
				strategy: tc.strategy,
				onChange: func(context.Context, Result) error { called = "reload"; return nil },
				execSelf: func() error { called = "restart"; return nil },
				terminate: func(string) (int, error) {
					called = "exit"
					return 42, nil
				},
			}
			if err := w.apply(context.Background(), result); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if called != tc.want {
				t.Fatalf("dispatched to %q, want %q", called, tc.want)
			}
		})
	}
}

func TestWatcherStrategyPropagatesErrors(t *testing.T) {
	sentinel := errors.New("strategy action failed")
	for _, tc := range []struct {
		name     string
		strategy ChangeStrategy
	}{
		{name: "reload", strategy: StrategyReload},
		{name: "restart", strategy: StrategyRestart},
		{name: "exit", strategy: StrategyExit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &Watcher{
				logger:   zap.NewNop(),
				strategy: tc.strategy,
				onChange: func(context.Context, Result) error { return sentinel },
				execSelf: func() error { return sentinel },
				terminate: func(string) (int, error) {
					return 0, sentinel
				},
			}
			if err := w.apply(context.Background(), Result{}); !errors.Is(err, sentinel) {
				t.Fatalf("apply error = %v, want the action error", err)
			}
		})
	}
}

func TestWatcherStrategyFromConfig(t *testing.T) {
	base := Config{
		Enabled: true,
		Addr:    "http://127.0.0.1:8848",
		DataID:  "gofy-service",
		File:    "config.yaml",
	}

	base.ChangeStrategy = StrategyExit
	w, err := NewWatcher(base)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if w.strategy != StrategyExit {
		t.Fatalf("strategy = %q, want exit from Config", w.strategy)
	}
	if w.execSelf == nil || w.terminate == nil {
		t.Fatal("watcher has no restart/exit actions installed")
	}

	base.ChangeStrategy = StrategyReload
	w, err = NewWatcher(base, WithRestartOnChange("app/demo.pid"))
	if err != nil {
		t.Fatalf("NewWatcher with option: %v", err)
	}
	if w.strategy != StrategyExit || w.restartPIDFile != "app/demo.pid" {
		t.Fatalf("WithRestartOnChange = %q/%q, want exit/app/demo.pid", w.strategy, w.restartPIDFile)
	}

	base.ChangeStrategy = "bogus"
	if _, err := NewWatcher(base); err == nil {
		t.Fatal("NewWatcher accepted an unknown strategy")
	}
}

func TestLoadConfigRestartGracePeriod(t *testing.T) {
	const plainApp = "nacos:\n  service_config:\n    data_id: gofy-service\n    group: GO_GROUP\n"
	cases := []struct {
		name    string
		appYAML string
		env     string
		want    time.Duration
	}{
		{name: "default is zero", appYAML: plainApp, want: 0},
		{name: "seconds from application file", appYAML: plainApp + "  restart_grace_period: 5\n", want: 5 * time.Second},
		{name: "seconds from environment", appYAML: plainApp, env: "7", want: 7 * time.Second},
		{name: "environment wins over file", appYAML: plainApp + "  restart_grace_period: 5\n", env: "7", want: 7 * time.Second},
		{name: "invalid environment is ignored", appYAML: plainApp, env: "abc", want: 0},
		{name: "negative file value is ignored", appYAML: plainApp + "  restart_grace_period: -3\n", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearNacosEnv(t)
			if tc.env != "" {
				t.Setenv("NACOS_RESTART_GRACE_PERIOD", tc.env)
			}
			dir := t.TempDir()
			nacosPath := writeTestFile(t, dir, "nacos.yaml", "nacos:\n  host: 127.0.0.1\n  namespace: dev\n")
			appPath := writeTestFile(t, dir, "application.yaml", tc.appYAML)

			cfg, err := Load(nacosPath, appPath)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.RestartGracePeriod != tc.want {
				t.Fatalf("RestartGracePeriod = %v, want %v", cfg.RestartGracePeriod, tc.want)
			}
		})
	}
}

func TestWatcherDrainAndGraceBeforeAction(t *testing.T) {
	const grace = 3 * time.Second
	cases := []struct {
		name     string
		strategy ChangeStrategy
		want     []string
	}{
		{name: "reload keeps serving", strategy: StrategyReload, want: []string{"reload"}},
		{name: "restart drains first", strategy: StrategyRestart, want: []string{"drain", "pause", "restart"}},
		{name: "exit drains first", strategy: StrategyExit, want: []string{"drain", "pause", "exit"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			w := &Watcher{
				logger:      zap.NewNop(),
				strategy:    tc.strategy,
				gracePeriod: grace,
				drain: func(context.Context) error {
					events = append(events, "drain")
					return nil
				},
				onChange: func(context.Context, Result) error {
					events = append(events, "reload")
					return nil
				},
				pause: func(_ context.Context, d time.Duration) bool {
					if d != grace {
						t.Errorf("pause duration = %v, want %v", d, grace)
					}
					events = append(events, "pause")
					return true
				},
				execSelf: func() error {
					events = append(events, "restart")
					return nil
				},
				terminate: func(string) (int, error) {
					events = append(events, "exit")
					return 42, nil
				},
			}
			if err := w.apply(context.Background(), Result{File: "config.yaml", Changed: true}); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if !reflect.DeepEqual(events, tc.want) {
				t.Fatalf("events = %v, want %v", events, tc.want)
			}
		})
	}
}

func TestWatcherDrainFailureStillRunsAction(t *testing.T) {
	sentinel := errors.New("deregister failed")
	acted := false
	w := &Watcher{
		logger:      zap.NewNop(),
		strategy:    StrategyRestart,
		drain:       func(context.Context) error { return sentinel },
		gracePeriod: time.Second,
		pause:       func(context.Context, time.Duration) bool { return true },
		execSelf: func() error {
			acted = true
			return nil
		},
	}
	if err := w.apply(context.Background(), Result{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !acted {
		t.Fatal("a drain hook error cancelled the restart")
	}
}

func TestWatcherCancelledContextSkipsAction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	acted := false
	w := &Watcher{
		logger:      zap.NewNop(),
		strategy:    StrategyRestart,
		gracePeriod: 5 * time.Second,
		pause:       sleep,
		execSelf: func() error {
			acted = true
			return nil
		},
	}
	if err := w.apply(ctx, Result{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("apply error = %v, want context.Canceled", err)
	}
	if acted {
		t.Fatal("restart ran after the grace period was cancelled")
	}
}

func TestWatcherGracePeriodFromConfig(t *testing.T) {
	cfg := Config{
		Enabled: true, Addr: "http://127.0.0.1:8848",
		DataID: "gofy-service", File: "config.yaml",
		RestartGracePeriod: 4 * time.Second,
	}
	w, err := NewWatcher(cfg)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if w.gracePeriod != 4*time.Second {
		t.Fatalf("gracePeriod = %v, want 4s", w.gracePeriod)
	}
	if w.pause == nil {
		t.Fatal("watcher has no pause function")
	}
}

// TestWatcherResumesAfterFailedAction is the P2-11 acceptance test: a failed
// exec or signal must not leave the process drained. The primary action is
// retried-then-abandoned, the resume hook runs, and the returned error still
// carries the original cause so the caller can log it.
// TestWatcherResumesAfterFailedAction 是 P2-11 的验收测试：exec 或发信号失败不能
// 让进程停在摘流状态。主动作失败后调用 resume 钩子，返回的错误仍携带原始原因，调用方
// 因此能记下来。
func TestWatcherResumesAfterFailedAction(t *testing.T) {
	sentinel := errors.New("exec failed")
	for _, tc := range []struct {
		name     string
		strategy ChangeStrategy
	}{
		{name: "restart", strategy: StrategyRestart},
		{name: "exit", strategy: StrategyExit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			w := &Watcher{
				logger:      zap.NewNop(),
				strategy:    tc.strategy,
				drain:       func(context.Context) error { events = append(events, "drain"); return nil },
				gracePeriod: 0,
				resume: func(context.Context) error {
					events = append(events, "resume")
					return nil
				},
				execSelf: func() error {
					events = append(events, "restart")
					return sentinel
				},
				terminate: func(string) (int, error) {
					events = append(events, "terminate")
					return 0, sentinel
				},
			}
			err := w.apply(context.Background(), Result{})
			if !errors.Is(err, sentinel) {
				t.Fatalf("apply error = %v, want the action error", err)
			}
			want := []string{"drain", "restart", "resume"}
			if tc.strategy == StrategyExit {
				want = []string{"drain", "terminate", "resume"}
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %v, want %v", events, want)
			}
		})
	}
}

// TestWatcherTerminatesWhenResumeFails covers the second half of P2-11: if the
// gate cannot be reopened there is no way back into service, so the watcher asks
// for termination instead of leaving a process that answers 503 forever.
// TestWatcherTerminatesWhenResumeFails 覆盖 P2-11 的另一半：闸门打不开就没有回到
// 服务的办法，watcher 因此请求终止，而不是留下一个永远 503 的进程。
func TestWatcherTerminatesWhenResumeFails(t *testing.T) {
	var events []string
	w := &Watcher{
		logger:      zap.NewNop(),
		strategy:    StrategyRestart,
		drain:       func(context.Context) error { events = append(events, "drain"); return nil },
		gracePeriod: 0,
		resume: func(context.Context) error {
			events = append(events, "resume")
			return errors.New("gate stuck")
		},
		execSelf: func() error {
			events = append(events, "restart")
			return errors.New("exec failed")
		},
		terminate: func(string) (int, error) {
			events = append(events, "terminate")
			return 42, nil
		},
	}
	if err := w.apply(context.Background(), Result{}); err == nil {
		t.Fatal("apply hid the failed action")
	}
	if want := []string{"drain", "restart", "resume", "terminate"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

// TestWatcherTerminatesWithoutResumeHook keeps the behaviour for hosts that
// wired only WithDrain: the action failed, the gate is closed, so exit.
// TestWatcherTerminatesWithoutResumeHook 保留只接了 WithDrain 的宿主的行为：动作
// 失败、闸门已关，于是退出。
func TestWatcherTerminatesWithoutResumeHook(t *testing.T) {
	terminated := 0
	w := &Watcher{
		logger:      zap.NewNop(),
		strategy:    StrategyRestart,
		drain:       func(context.Context) error { return nil },
		gracePeriod: 0,
		execSelf:    func() error { return errors.New("exec failed") },
		terminate: func(string) (int, error) {
			terminated++
			return 42, nil
		},
	}
	if err := w.apply(context.Background(), Result{}); err == nil {
		t.Fatal("apply hid the failed action")
	}
	if terminated != 1 {
		t.Fatalf("terminate calls = %d, want 1", terminated)
	}
}
