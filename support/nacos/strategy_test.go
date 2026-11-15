package nacos

import (
	"context"
	"errors"
	"strings"
	"testing"

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
