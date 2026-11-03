package config_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gofyconfig "github.com/bingcool/gofy/config"
)

type subConfig struct {
	Timeout time.Duration `yaml:"timeout" default:"3s"`
	Max     int           `yaml:"max" default:"10" validate:"min=1"`
}

type testConfig struct {
	Name  string    `yaml:"name" default:"default-name"`
	DSN   string    `yaml:"dsn"`
	Sub   subConfig `yaml:"sub"`
	Plain string    `yaml:"plain"`
}

func writeFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadAppliesDefaultsAndDecodes(t *testing.T) {
	path := writeFile(t, "dsn: user:pass@tcp(db:3306)/app\nsub:\n  max: 5\n")

	cfg, err := gofyconfig.Load[testConfig](context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Name != "default-name" {
		t.Fatalf("name = %q, want default", cfg.Name)
	}
	if cfg.Sub.Timeout != 3*time.Second {
		t.Fatalf("timeout = %s, want 3s", cfg.Sub.Timeout)
	}
	if cfg.Sub.Max != 5 {
		t.Fatalf("max = %d, want 5", cfg.Sub.Max)
	}
}

func TestLoadExpandsEnvironmentVariables(t *testing.T) {
	t.Setenv("GOFY_TEST_DSN", "user:secret@tcp(db:3306)/app")
	path := writeFile(t, "dsn: ${GOFY_TEST_DSN}\n")

	cfg, err := gofyconfig.Load[testConfig](context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DSN != "user:secret@tcp(db:3306)/app" {
		t.Fatalf("dsn = %q, want expanded value", cfg.DSN)
	}
}

func TestLoadFailsOnMissingEnvironmentVariable(t *testing.T) {
	path := writeFile(t, "dsn: ${GOFY_TEST_UNSET_DSN}\n")

	_, err := gofyconfig.Load[testConfig](context.Background(), path)
	if err == nil {
		t.Fatal("Load accepted a missing environment variable")
	}
	if !strings.Contains(err.Error(), "GOFY_TEST_UNSET_DSN") {
		t.Fatalf("error does not name the missing variable: %v", err)
	}
}

func TestLoadAllowMissingEnvironmentVariable(t *testing.T) {
	path := writeFile(t, "dsn: ${GOFY_TEST_UNSET_DSN}\n")

	cfg, err := gofyconfig.Load[testConfig](context.Background(), path, gofyconfig.WithAllowMissingEnv(true))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DSN != "" {
		t.Fatalf("dsn = %q, want empty", cfg.DSN)
	}
}

func TestLoadEscapedDollar(t *testing.T) {
	path := writeFile(t, "dsn: '$${NOT_A_VAR}'\n")

	cfg, err := gofyconfig.Load[testConfig](context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DSN != "${NOT_A_VAR}" {
		t.Fatalf("dsn = %q, want literal ${NOT_A_VAR}", cfg.DSN)
	}
}

func TestLoadStrictRejectsUnknownFields(t *testing.T) {
	path := writeFile(t, "unknown_key: true\n")

	_, err := gofyconfig.Load[testConfig](context.Background(), path, gofyconfig.WithStrict(true))
	if err == nil {
		t.Fatal("strict mode accepted an unknown field")
	}
	if !strings.Contains(err.Error(), "unknown_key") {
		t.Fatalf("error does not name the unknown key: %v", err)
	}

	if _, err := gofyconfig.Load[testConfig](context.Background(), path); err != nil {
		t.Fatalf("non-strict Load: %v", err)
	}
}

func TestLoadRunsValidation(t *testing.T) {
	path := writeFile(t, "sub:\n  max: 0\n")

	_, err := gofyconfig.Load[testConfig](context.Background(), path)
	if err == nil {
		t.Fatal("Load accepted an invalid config")
	}

	if _, err := gofyconfig.Load[testConfig](context.Background(), path, gofyconfig.WithValidate(false)); err != nil {
		t.Fatalf("Load with validation disabled: %v", err)
	}
}

func TestLoadWithoutDefaults(t *testing.T) {
	path := writeFile(t, "name: explicit\n")

	cfg, err := gofyconfig.Load[testConfig](context.Background(), path,
		gofyconfig.WithApplyDefaults(false), gofyconfig.WithValidate(false))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Name != "explicit" {
		t.Fatalf("name = %q, want explicit", cfg.Name)
	}
	if cfg.Sub.Timeout != 0 {
		t.Fatalf("timeout = %s, want zero when defaults are disabled", cfg.Sub.Timeout)
	}
}

func TestLoadRejectsMissingPathAndCancelledContext(t *testing.T) {
	if _, err := gofyconfig.Load[testConfig](context.Background(), "  "); err == nil {
		t.Fatal("Load accepted an empty path")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gofyconfig.Load[testConfig](ctx, writeFile(t, "name: x\n")); err == nil {
		t.Fatal("Load ignored a cancelled context")
	}
}
