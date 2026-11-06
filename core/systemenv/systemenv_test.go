package systemenv_test

import (
	"runtime"
	"testing"

	"github.com/bingcool/gofy/core/systemenv"
)

func TestGetEnv(t *testing.T) {
	cases := map[string]struct {
		value string
		want  string
	}{
		"unset":      {"", systemenv.EnvDev},
		"dev":        {"dev", systemenv.EnvDev},
		"test":       {"test", systemenv.EnvTest},
		"grad":       {"grad", systemenv.EnvGrad},
		"prod":       {"prod", systemenv.EnvProd},
		"normalized": {"  PROD\t", systemenv.EnvProd},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("GOFY_ENV", tc.value)
			if got := systemenv.GetEnv(); got != tc.want {
				t.Fatalf("GetEnv() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEnvPredicates(t *testing.T) {
	checks := []struct {
		env   string
		match func() bool
	}{
		{systemenv.EnvDev, systemenv.IsDev},
		{systemenv.EnvTest, systemenv.IsTest},
		{systemenv.EnvGrad, systemenv.IsGrad},
		{systemenv.EnvProd, systemenv.IsProd},
	}
	for _, current := range checks {
		t.Setenv("GOFY_ENV", current.env)
		for _, check := range checks {
			want := check.env == current.env
			if got := check.match(); got != want {
				t.Fatalf("GOFY_ENV=%s: predicate for %s = %v, want %v", current.env, check.env, got, want)
			}
		}
	}
}

func TestOSPredicates(t *testing.T) {
	checks := map[string]func() bool{
		"windows": systemenv.IsWindows,
		"linux":   systemenv.IsLinux,
		"darwin":  systemenv.IsMacos,
	}
	for goos, check := range checks {
		want := runtime.GOOS == goos
		if got := check(); got != want {
			t.Fatalf("predicate for %s on %s = %v, want %v", goos, runtime.GOOS, got, want)
		}
	}
}

func TestGetGoroutineID(t *testing.T) {
	id := systemenv.GetGoroutineID()
	if id == 0 {
		t.Fatal("GetGoroutineID() = 0")
	}
	if again := systemenv.GetGoroutineID(); again != id {
		t.Fatalf("ID changed within the same goroutine: %d then %d", id, again)
	}

	other := make(chan uint64, 1)
	go func() { other <- systemenv.GetGoroutineID() }()
	if got := <-other; got == 0 || got == id {
		t.Fatalf("other goroutine ID = %d, current = %d", got, id)
	}
}
