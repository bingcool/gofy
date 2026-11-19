package sqllog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gopkg.in/yaml.v3"
)

func boolPtr(v bool) *bool { return &v }

func TestConfigResolveEnabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "absent section", cfg: Config{}, want: false},
		{name: "empty mapping", cfg: Config{keys: map[string]struct{}{}}, want: false},
		{name: "explicit false with other keys", cfg: Config{
			Enabled: boolPtr(false),
			Mode:    "full",
			keys:    map[string]struct{}{"enabled": {}, "mode": {}},
		}, want: false},
		{name: "explicit true", cfg: Config{Enabled: boolPtr(true)}, want: true},
		{name: "mode only", cfg: Config{
			Mode: "full",
			keys: map[string]struct{}{"mode": {}},
		}, want: true},
		{name: "explicit zero value counts as written", cfg: Config{
			keys: map[string]struct{}{"log_schema_queries": {}},
		}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.ResolveEnabled(); got != tc.want {
				t.Fatalf("ResolveEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestConfigResolveEnabledForProgrammaticConfig is the regression for the audit
// finding: a Config built in code used to resolve to disabled no matter what
// was set, because keys only exists on the YAML path.
// TestConfigResolveEnabledForProgrammaticConfig 是审计结论的回归测试：代码里构造的
// Config 原先无论填了什么都会解析为关闭，因为 keys 只存在于 YAML 路径上。
func TestConfigResolveEnabledForProgrammaticConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "zero value stays off", cfg: Config{}, want: false},
		{name: "mode", cfg: Config{Mode: "full"}, want: true},
		{name: "mode off still enables the section", cfg: Config{Mode: string(ModeOff)}, want: true},
		{name: "sample rate", cfg: Config{SampleRate: 0.5}, want: true},
		{name: "negative max length means no truncation", cfg: Config{MaxLength: -1}, want: true},
		{name: "schema queries", cfg: Config{LogSchemaQueries: true}, want: true},
		{name: "error rate limit", cfg: Config{ErrorRateLimit: -1}, want: true},
		{name: "slow rate limit", cfg: Config{SlowRateLimit: 5}, want: true},
		{name: "mask strategy", cfg: Config{Mask: MaskConfig{Strategy: "mask"}}, want: true},
		{name: "mask columns", cfg: Config{Mask: MaskConfig{Columns: []string{"employee_no"}}}, want: true},
		{name: "mask patterns", cfg: Config{Mask: MaskConfig{Patterns: []string{"re:^ORD"}}}, want: true},
		{name: "explicit false beats values", cfg: Config{Enabled: boolPtr(false), Mode: "full"}, want: false},
		{name: "explicit true alone", cfg: Config{Enabled: boolPtr(true)}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.ResolveEnabled(); got != tc.want {
				t.Fatalf("ResolveEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestConfigYAMLAndStructAgree is the equivalence test the audit asked for: the
// same configuration must resolve the same way whether it came from YAML or
// from a struct literal.
// TestConfigYAMLAndStructAgree 是审计要求的等价测试：同一份配置无论来自 YAML 还是
// 结构体字面量，都必须解析出同一个结果。
func TestConfigYAMLAndStructAgree(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		code Config
		want bool
	}{
		{name: "empty mapping", doc: "{}\n", code: Config{}, want: false},
		{name: "mode only", doc: "mode: full\n", code: Config{Mode: "full"}, want: true},
		{name: "sample rate only", doc: "sample_rate: 0.5\n", code: Config{SampleRate: 0.5}, want: true},
		{name: "explicit off with values", doc: "enabled: false\nmode: full\n",
			code: Config{Enabled: boolPtr(false), Mode: "full"}, want: false},
		{name: "explicit on", doc: "enabled: true\n", code: Config{Enabled: boolPtr(true)}, want: true},
		{name: "mask only", doc: "mask:\n  columns: [employee_no]\n",
			code: Config{Mask: MaskConfig{Columns: []string{"employee_no"}}}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fromYAML Config
			if err := yaml.Unmarshal([]byte(tc.doc), &fromYAML); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := fromYAML.ResolveEnabled(); got != tc.want {
				t.Fatalf("YAML ResolveEnabled() = %v, want %v", got, tc.want)
			}
			if got := tc.code.ResolveEnabled(); got != tc.want {
				t.Fatalf("struct ResolveEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfigNormalizeFillsDefaults(t *testing.T) {
	cfg, err := Config{}.normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if cfg.Mode != string(ModePlaceholder) {
		t.Fatalf("mode = %q, want placeholder", cfg.Mode)
	}
	if cfg.SampleRate != DefaultSQLSampleRate {
		t.Fatalf("sample rate = %v, want %v", cfg.SampleRate, DefaultSQLSampleRate)
	}
	if cfg.MaxLength != DefaultSQLMaxLength {
		t.Fatalf("max length = %d, want %d", cfg.MaxLength, DefaultSQLMaxLength)
	}
	if cfg.ErrorRateLimit != DefaultSQLErrorRateLimit {
		t.Fatalf("error rate limit = %d, want %d", cfg.ErrorRateLimit, DefaultSQLErrorRateLimit)
	}
}

func TestConfigSentinels(t *testing.T) {
	cfg, err := Config{MaxLength: -1, ErrorRateLimit: -1, SampleRate: 1}.normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if cfg.MaxLength != -1 || cfg.ErrorRateLimit != -1 || cfg.SampleRate != 1 {
		t.Fatalf("sentinels lost: %+v", cfg)
	}
	if _, err := (Config{MaxLength: -2}).normalize(); err == nil {
		t.Fatal("max_length -2 accepted, want error")
	}
	if _, err := (Config{Mode: "verbose"}).normalize(); err == nil {
		t.Fatal("unknown mode accepted, want error")
	}
}

func TestPolicyDisabledIgnoresInvalidValues(t *testing.T) {
	// A switched-off section must not fail startup even when it holds a stale
	// value: nothing takes effect, so nothing is validated.
	policy, err := NewPolicy(Config{
		Enabled: boolPtr(false),
		Mode:    "verbose",
		keys:    map[string]struct{}{"enabled": {}},
	})
	if err != nil {
		t.Fatalf("disabled policy rejected: %v", err)
	}
	if !policy.Disabled() {
		t.Fatal("policy should be disabled")
	}
}

func TestPolicyOffModeKeepsFailuresWithoutText(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	policy, err := NewPolicy(Config{
		Mode:           "off",
		ErrorRateLimit: -1,
		keys:           map[string]struct{}{"mode": {}},
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	gl := GormLogger(zap.New(core), GormLoggerConfig{SlowThreshold: time.Nanosecond, Policy: policy})

	gl.Trace(context.Background(), time.Now(), func() (string, int64) { return "SELECT 1", 1 }, errors.New("boom"))
	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if _, ok := fields["sql"]; ok {
		t.Fatalf("off mode leaked sql field: %v", fields["sql"])
	}
	if fields["sql_mode"] != "off" {
		t.Fatalf("sql_mode = %v, want off", fields["sql_mode"])
	}
}

func TestPolicyDisabledProducesNothingAndSkipsFC(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	policy, err := NewPolicy(Config{})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	gl := GormLogger(zap.New(core), GormLoggerConfig{SlowThreshold: time.Nanosecond, Policy: policy})

	called := false
	gl.Trace(context.Background(), time.Now(), func() (string, int64) {
		called = true
		return "SELECT 1", 1
	}, errors.New("boom"))

	if len(observed.All()) != 0 {
		t.Fatalf("disabled policy wrote %d entries", len(observed.All()))
	}
	if called {
		t.Fatal("disabled policy called fc(); the statement must not be rendered")
	}
}

func TestPolicyMasksExpandedStatement(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	policy, err := NewPolicy(Config{
		Mode:       "full",
		SampleRate: 1,
		Mask:       MaskConfig{Enabled: true},
		keys:       map[string]struct{}{"mode": {}},
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	// No slow threshold: this case is about rendering, and a 1ns threshold can
	// still miss on a coarse clock, which would make the test flaky.
	// 不设慢阈值：这个用例关心的是渲染，而 1ns 的阈值在粗粒度时钟上仍可能落空，
	// 那样测试就会不稳定。
	// Info rows need log_level: info; the default warn only reports slow and
	// failed statements.
	// Info 行需要 log_level: info；默认的 warn 只上报慢与失败语句。
	gl := GormLogger(zap.New(core), GormLoggerConfig{LogLevel: "info", Policy: policy})

	gl.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT * FROM users WHERE phone = '13800138000' AND name = 'bob'", 1
	}, nil)

	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	sql, _ := entry.ContextMap()["sql"].(string)
	if strings.Contains(sql, "13800138000") {
		t.Fatalf("phone leaked: %s", sql)
	}
	if !strings.Contains(sql, "138****8000") {
		t.Fatalf("phone not masked in shape: %s", sql)
	}
	if !strings.Contains(sql, "'bob'") {
		t.Fatalf("non-sensitive value was rewritten: %s", sql)
	}
	if entry.ContextMap()["sql_masked"] != true {
		t.Fatalf("sql_masked = %v, want true", entry.ContextMap()["sql_masked"])
	}
}

func TestPolicyRateLimitDropsAndSummarizes(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	policy, err := NewPolicy(Config{
		Mode:           "full",
		ErrorRateLimit: 2,
		keys:           map[string]struct{}{"mode": {}},
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	gl := GormLogger(zap.New(core), GormLoggerConfig{Policy: policy})

	for i := 0; i < 5; i++ {
		gl.Trace(context.Background(), time.Now(), func() (string, int64) { return "SELECT 1", 0 }, errors.New("boom"))
	}
	errors := 0
	for _, entry := range observed.All() {
		if entry.Message == "sql error" {
			errors++
		}
	}
	if errors != 2 {
		t.Fatalf("sql error entries = %d, want 2", errors)
	}
}
