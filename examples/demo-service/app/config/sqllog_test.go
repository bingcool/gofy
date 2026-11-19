package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestSQLLogSectionIsOptional pins the contract the design doc promises: the
// section, its mask child and their lists can all be absent, and their absence
// must not fail startup. It also pins the "wrote anything means enabled" rule,
// which cannot be expressed by zero values alone.
// TestSQLLogSectionIsOptional 固定设计文档承诺的契约：整段、mask 子段与两个列表都可以
// 缺席，缺席不得导致启动失败；同时固定"写了就算启用"——这条规则无法只靠零值表达。
func TestSQLLogSectionIsOptional(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		enabled bool
	}{
		{name: "section absent", body: "", enabled: false},
		{name: "empty mapping", body: "sql_log: {}\n", enabled: false},
		{name: "explicitly disabled", body: "sql_log:\n  enabled: false\n  mode: full\n", enabled: false},
		{name: "mode only", body: "sql_log:\n  mode: full\n", enabled: true},
		{name: "explicitly enabled", body: "sql_log:\n  enabled: true\n", enabled: true},
		{name: "explicit zero value counts", body: "sql_log:\n  log_schema_queries: false\n", enabled: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			body := "env: dev\n" + tc.body
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			cfg, err := Load(context.Background(), path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := cfg.SQLLog.ResolveEnabled(); got != tc.enabled {
				t.Fatalf("ResolveEnabled() = %v, want %v", got, tc.enabled)
			}
		})
	}
}
