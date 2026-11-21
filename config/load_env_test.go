package config_test

import (
	"context"
	"strings"
	"testing"

	gofyconfig "github.com/bingcool/gofy/config"
)

// TestLoadKeepsMultilineValueInsideItsScalar: a PEM block in an environment
// variable used to be spliced into the document, so every line after the first
// became YAML structure. The value must stay one scalar.
// TestLoadKeepsMultilineValueInsideItsScalar：环境变量里的 PEM 块过去会被拼进
// 文档，第一行之后的每一行都变成 YAML 结构。该值必须保持单个标量。
func TestLoadKeepsMultilineValueInsideItsScalar(t *testing.T) {
	pem := "-----BEGIN KEY-----\nMIIB\n-----END KEY-----"
	t.Setenv("GOFY_TEST_PEM", pem)
	path := writeFile(t, "dsn: ${GOFY_TEST_PEM}\nplain: after\n")

	cfg, err := gofyconfig.Load[testConfig](context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DSN != pem {
		t.Fatalf("dsn = %q, want the whole PEM block", cfg.DSN)
	}
	if cfg.Plain != "after" {
		t.Fatalf("plain = %q, want %q: the following key moved", cfg.Plain, "after")
	}
}

// TestLoadKeepsCommentMarkerInValue: " #" used to start a YAML comment, so the
// tail of a password was dropped while the file still parsed.
// TestLoadKeepsCommentMarkerInValue：" #" 过去会开启 YAML 注释，口令的后半段被
// 丢掉，而文件仍然解析成功。
func TestLoadKeepsCommentMarkerInValue(t *testing.T) {
	const password = "pa ss #word:tail"
	t.Setenv("GOFY_TEST_PASSWORD", password)
	path := writeFile(t, "dsn: ${GOFY_TEST_PASSWORD}\n")

	cfg, err := gofyconfig.Load[testConfig](context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DSN != password {
		t.Fatalf("dsn = %q, want %q", cfg.DSN, password)
	}
}

// TestLoadKeepsStructureCharactersInValue covers the other shapes that changed
// the document: a colon-space and a leading dash.
// TestLoadKeepsStructureCharactersInValue 覆盖其余会改写文档的形态：冒号加空格与
// 开头的短横。
func TestLoadKeepsStructureCharactersInValue(t *testing.T) {
	cases := map[string]string{
		"colon":  "host: 5432",
		"dash":   "- not a list item",
		"braces": "{not: a map}",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("GOFY_TEST_VALUE", value)
			path := writeFile(t, "dsn: ${GOFY_TEST_VALUE}\n")
			cfg, err := gofyconfig.Load[testConfig](context.Background(), path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.DSN != value {
				t.Fatalf("dsn = %q, want %q", cfg.DSN, value)
			}
		})
	}
}

type numericConfig struct {
	Port int `yaml:"port"`
}

// TestLoadInfersTypeAfterSubstitution: expanding before parsing let "port:
// ${PORT}" become an int. Expanding inside the scalar has to keep that, or
// every numeric environment variable breaks.
// TestLoadInfersTypeAfterSubstitution：解析前替换让 "port: ${PORT}" 能成为 int。
// 在标量内替换必须保持这一点，否则所有数字型环境变量都会坏掉。
func TestLoadInfersTypeAfterSubstitution(t *testing.T) {
	t.Setenv("GOFY_TEST_PORT", "8080")
	path := writeFile(t, "port: ${GOFY_TEST_PORT}\n")

	cfg, err := gofyconfig.Load[numericConfig](context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 8080 {
		t.Fatalf("port = %d, want 8080", cfg.Port)
	}
}

// TestLoadQuotedSubstitutionStaysAString: quoting is the caller's way of saying
// "this is text". A numeric-looking value must not turn into a number.
// TestLoadQuotedSubstitutionStaysAString：引号是调用方"这是文本"的表达方式，
// 数字形态的值不能因此变成数字。
func TestLoadQuotedSubstitutionStaysAString(t *testing.T) {
	t.Setenv("GOFY_TEST_QUOTED", "8080")
	path := writeFile(t, "dsn: \"${GOFY_TEST_QUOTED}\"\n")

	cfg, err := gofyconfig.Load[testConfig](context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DSN != "8080" {
		t.Fatalf("dsn = %q, want the string \"8080\"", cfg.DSN)
	}
}

// TestLoadAllowMissingLeavesAnEmptyString: an absent variable is a known gap,
// not a reason to reshape the document. The scalar stays a string.
// TestLoadAllowMissingLeavesAnEmptyString：变量缺失是已知情况，不是改写文档的
// 理由。该标量仍然是字符串。
func TestLoadAllowMissingLeavesAnEmptyString(t *testing.T) {
	path := writeFile(t, "dsn: ${GOFY_TEST_UNSET_KEEP}\nplain: kept\n")

	cfg, err := gofyconfig.Load[testConfig](context.Background(), path,
		gofyconfig.WithAllowMissingEnv(true))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DSN != "" {
		t.Fatalf("dsn = %q, want empty", cfg.DSN)
	}
	if cfg.Plain != "kept" {
		t.Fatalf("plain = %q, want kept", cfg.Plain)
	}
}

// TestLoadRejectsAFoldedValueThatWouldChangeStructure is the negative case:
// the file itself is fine, and the substituted value must not sneak a new key
// into it.
// TestLoadRejectsAFoldedValueThatWouldChangeStructure 是反向用例：文件本身没问题，
// 替换进来的值不能夹带新的键。
func TestLoadRejectsAFoldedValueThatWouldChangeStructure(t *testing.T) {
	t.Setenv("GOFY_TEST_INJECT", "value\nsub:\n  max: 99")
	path := writeFile(t, "dsn: ${GOFY_TEST_INJECT}\nsub:\n  max: 1\n")

	cfg, err := gofyconfig.Load[testConfig](context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sub.Max != 1 {
		t.Fatalf("sub.max = %d, want 1: the substituted value changed the document", cfg.Sub.Max)
	}
	if !strings.Contains(cfg.DSN, "sub:") {
		t.Fatalf("dsn = %q, want the injected text kept as data", cfg.DSN)
	}
}
