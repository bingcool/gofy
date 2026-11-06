package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDatabasesByInstanceName(t *testing.T) {
	path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
  report:
    dsn: user:pass@tcp(127.0.0.1:3306)/report
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Database(DefaultDatabase).DSN; got != "user:pass@tcp(127.0.0.1:3306)/main" {
		t.Fatalf("default dsn = %q", got)
	}
	if got := cfg.Database("report").DSN; got != "user:pass@tcp(127.0.0.1:3306)/report" {
		t.Fatalf("report dsn = %q", got)
	}
}

func TestLoadRequiresDefaultDatabase(t *testing.T) {
	path := writeConfig(t, `
db:
  report:
    dsn: user:pass@tcp(127.0.0.1:3306)/report
`)

	if _, err := Load(context.Background(), path); err == nil {
		t.Fatal("Load accepted config without db.default")
	}
}

func TestLoadRedisInstancesByInstanceName(t *testing.T) {
	path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
redis:
  cache:
    enabled: true
    addr: 127.0.0.1:6379
    db: 2
  session:
    enabled: false
    addr: 127.0.0.1:6379
  ratelimit:
    enabled: true
    addr: 127.0.0.1:6379
    password: test-pass
    db: 3
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cache := cfg.Redis["cache"]
	if !cache.Enabled || cache.Addr != "127.0.0.1:6379" || cache.DB != 2 {
		t.Fatalf("cache redis = %+v", cache)
	}
	if cfg.Redis["session"].Enabled {
		t.Fatal("session redis must stay disabled")
	}
	if limit := cfg.Redis["ratelimit"]; !limit.Enabled || limit.Addr != "127.0.0.1:6379" ||
		limit.Password != "test-pass" || limit.DB != 3 {
		t.Fatalf("ratelimit redis = %+v", limit)
	}
}

func TestLoadRejectsEnabledRedisWithoutAddr(t *testing.T) {
	path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
redis:
  cache:
    enabled: true
`)

	if _, err := Load(context.Background(), path); err == nil {
		t.Fatal("Load accepted enabled redis without addr")
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestApplyLogPathsFollowsServiceMode(t *testing.T) {
	original := os.Args
	defer func() { os.Args = original }()

	cases := []struct {
		name   string
		args   []string
		dir    string
		info   string
		error  string
		system string
	}{
		{"http start", []string{"start"}, filepath.Join("storage", "log", "http"), "info.log", "error.log", "system_error.log"},
		{"http stop", []string{"stop"}, filepath.Join("storage", "log", "http"), "info.log", "error.log", "system_error.log"},
		{"script", []string{"script", "fix-order", "--order-id=1"}, filepath.Join("storage", "log", "script", "{date}"), filepath.Join("info", "fix-order.log"), filepath.Join("error", "fix-order.log"), filepath.Join("error", "fix-order-system_error.log")},
		{"script spaces removed", []string{"script", "Fix Order"}, filepath.Join("storage", "log", "script", "{date}"), filepath.Join("info", "FixOrder.log"), filepath.Join("error", "FixOrder.log"), filepath.Join("error", "FixOrder-system_error.log")},
		{"script path characters removed", []string{"script", "../../etc/passwd"}, filepath.Join("storage", "log", "script", "{date}"), filepath.Join("info", "etcpasswd.log"), filepath.Join("error", "etcpasswd.log"), filepath.Join("error", "etcpasswd-system_error.log")},
		{"script list", []string{"script", "--list"}, filepath.Join("storage", "log", "script", "{date}"), filepath.Join("info", "info.log"), filepath.Join("error", "error.log"), filepath.Join("error", "system_error.log")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			os.Args = append([]string{"demo-service"}, tc.args...)
			cfg := &Config{}
			applyLogPaths(cfg)

			checks := []struct {
				name string
				got  string
				want string
			}{
				{"info", cfg.Log.Outputs[len(cfg.Log.Outputs)-1], filepath.Join(tc.dir, tc.info)},
				{"error", cfg.Log.ErrorOutputs[len(cfg.Log.ErrorOutputs)-1], filepath.Join(tc.dir, tc.error)},
				{"system", cfg.Log.SystemErrorOutputs[len(cfg.Log.SystemErrorOutputs)-1], filepath.Join(tc.dir, tc.system)},
			}
			for _, check := range checks {
				if check.got != check.want {
					t.Fatalf("%s log = %q, want %q", check.name, check.got, check.want)
				}
			}
		})
	}
}

func TestApplyLogPathsKeepsConfiguredOutputs(t *testing.T) {
	original := os.Args
	defer func() { os.Args = original }()
	os.Args = []string{"demo-service"}

	cfg := &Config{}
	cfg.Log.Outputs = []string{"stdout"}
	applyLogPaths(cfg)

	if len(cfg.Log.Outputs) != 2 || cfg.Log.Outputs[0] != "stdout" {
		t.Fatalf("outputs = %v", cfg.Log.Outputs)
	}
}
