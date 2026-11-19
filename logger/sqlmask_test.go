package logger

import (
	"strings"
	"testing"
)

func mustSet(t *testing.T, cfg MaskConfig) maskSet {
	t.Helper()
	set, err := defaultMaskSet().merge(cfg)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	return set
}

func TestMaskSetMergeAppendsToBuiltins(t *testing.T) {
	set := mustSet(t, MaskConfig{
		Strategy: "hash",
		Columns:  []string{"Employee_No", "employee-no"},
		Patterns: []string{"email", `re:^ORD[0-9]{12}$`},
	})
	if _, ok := set.columns[normalizeColumn("password")]; !ok {
		t.Fatal("built-in column disappeared after merge")
	}
	if _, ok := set.columns[normalizeColumn("employee_no")]; !ok {
		t.Fatal("appended column not normalized into the set")
	}
	if set.strategy != MaskStrategyHash {
		t.Fatalf("strategy = %q, want hash", set.strategy)
	}
	// Built-in patterns plus the custom one; a duplicate name must not add a
	// second scanner for the same shape.
	if len(set.patterns) < len(DefaultMaskPatterns)+1 {
		t.Fatalf("patterns = %d, want at least %d", len(set.patterns), len(DefaultMaskPatterns)+1)
	}
}

func TestMaskSetRejectsUnknownPatternAndStrategy(t *testing.T) {
	if _, err := defaultMaskSet().merge(MaskConfig{Patterns: []string{"ipv4"}}); err == nil {
		t.Fatal("unknown pattern accepted, want error")
	}
	if _, err := defaultMaskSet().merge(MaskConfig{Strategy: "blur"}); err == nil {
		t.Fatal("unknown strategy accepted, want error")
	}
	if _, err := defaultMaskSet().merge(MaskConfig{Patterns: []string{"re:("}}); err == nil {
		t.Fatal("invalid regex accepted, want error")
	}
}

func TestMaskSQLMasksColumnValue(t *testing.T) {
	set := mustSet(t, MaskConfig{})
	cases := []struct {
		name   string
		in     string
		secret string
		want   string
	}{
		{
			name:   "equality",
			in:     "SELECT * FROM users WHERE phone = '13800138000'",
			secret: "13800138000",
			want:   "138****8000",
		},
		{
			name:   "backquoted column",
			in:     "SELECT * FROM users WHERE `phone` = '13800138000'",
			secret: "13800138000",
			want:   "138****8000",
		},
		{
			name:   "in list",
			in:     "SELECT * FROM users WHERE mobile IN ('13800138000','13900139000')",
			secret: "13800138000",
			want:   "138****8000",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maskSQL(tc.in, set)
			if strings.Contains(got, tc.secret) {
				t.Fatalf("value leaked: %s", got)
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("masked text = %s, want to contain %s", got, tc.want)
			}
		})
	}
}

func TestMaskSQLKeepsNonSensitiveValues(t *testing.T) {
	set := mustSet(t, MaskConfig{})
	in := "SELECT * FROM users WHERE name = 'bob' AND status = 'paid'"
	if got := maskSQL(in, set); got != in {
		t.Fatalf("statement rewritten: %s", got)
	}
}

func TestMaskSQLValuePatternFallback(t *testing.T) {
	set := mustSet(t, MaskConfig{})
	// No column name says "phone": the value shape has to catch it.
	in := "SELECT * FROM notes WHERE remark LIKE '%13800138000%'"
	got := maskSQL(in, set)
	if strings.Contains(got, "13800138000") {
		t.Fatalf("phone in free text leaked: %s", got)
	}
}

func TestMaskSQLHandlesEscapedLiteral(t *testing.T) {
	set := mustSet(t, MaskConfig{})
	in := "SELECT * FROM users WHERE phone = '13800138000' AND note = 'a''b, c)'"
	got := maskSQL(in, set)
	if strings.Contains(got, "13800138000") {
		t.Fatalf("phone leaked: %s", got)
	}
	if !strings.Contains(got, "'a''b, c)'") {
		t.Fatalf("escaped literal was cut: %s", got)
	}
}

func TestMaskValueStrategies(t *testing.T) {
	if got := maskValue("13800138000", MaskStrategyMask); got != "138****8000" {
		t.Fatalf("mask = %q", got)
	}
	if got := maskValue("short", MaskStrategyMask); got != "***" {
		t.Fatalf("short value = %q, want ***", got)
	}
	if got := maskValue("13800138000", MaskStrategyRemove); got != "***" {
		t.Fatalf("remove = %q", got)
	}
	first := maskValue("13800138000", MaskStrategyHash)
	second := maskValue("13800138000", MaskStrategyHash)
	if first != second || !strings.HasPrefix(first, "sha256:") || len(first) != len("sha256:")+8 {
		t.Fatalf("hash = %q / %q, want stable sha256 prefix", first, second)
	}
}

func TestNormalizeColumn(t *testing.T) {
	for _, name := range []string{"api_key", "apiKey", "API-KEY", "`api_key`"} {
		if got := normalizeColumn(name); got != "apikey" {
			t.Fatalf("normalizeColumn(%q) = %q, want apikey", name, got)
		}
	}
}

func TestIsSchemaQuery(t *testing.T) {
	for _, sql := range []string{
		"SHOW FULL COLUMNS FROM `orders`",
		"SELECT * FROM information_schema.tables",
	} {
		if !isSchemaQuery(sql) {
			t.Fatalf("isSchemaQuery(%q) = false, want true", sql)
		}
	}
	if isSchemaQuery("SELECT * FROM orders") {
		t.Fatal("business query treated as schema introspection")
	}
}
