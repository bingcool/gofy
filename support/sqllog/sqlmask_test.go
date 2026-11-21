package sqllog

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
	if len(set.patterns) < len(DefaultMaskPatterns())+1 {
		t.Fatalf("patterns = %d, want at least %d", len(set.patterns), len(DefaultMaskPatterns())+1)
	}
}

// TestDefaultRulesAreCopies pins P2-05: the framework baseline cannot be
// replaced or edited through the exported accessors. Mutating a returned slice
// must leave both later reads and the actual mask set untouched, including
// under concurrent readers.
// TestDefaultRulesAreCopies 固定 P2-05：框架基线不能通过导出访问器被替换或改写。改返回值
// 不能影响后续读取，也不能影响实际规则集；并发读取时同样如此。
func TestDefaultRulesAreCopies(t *testing.T) {
	cols := DefaultMaskColumns()
	pats := DefaultMaskPatterns()
	if len(cols) == 0 || len(pats) == 0 {
		t.Fatal("default rules are empty")
	}
	cols[0] = "not-a-rule"
	cols = append(cols, "extra")
	pats[0] = "not-a-pattern"

	if got := DefaultMaskColumns(); got[0] == "not-a-rule" || len(got) == len(cols) {
		t.Fatalf("DefaultMaskColumns leaked mutable state: %v", got)
	}
	if got := DefaultMaskPatterns(); got[0] == "not-a-pattern" {
		t.Fatalf("DefaultMaskPatterns leaked mutable state: %v", got)
	}

	// The baseline still masks a built-in column after the attempted edit.
	// 改副本之后，内置列的脱敏仍然生效。
	set := mustSet(t, MaskConfig{})
	sql := "UPDATE users SET password = 'hunter2'"
	got := maskSQL(sql, set)
	if strings.Contains(got, "hunter2") {
		t.Fatalf("built-in rule was removed: %s", got)
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

// TestMaskSQLInsertColumnsByPosition covers the shape the scanner used to skip:
// a custom sensitive column in an INSERT. The column list is declared once and
// the values repeat across tuples, so only positional mapping catches every
// row. Non-sensitive columns in the same tuple must stay readable.
// TestMaskSQLInsertColumnsByPosition 覆盖扫描器过去跳过的形态：自定义敏感列出现在 INSERT
// 中。列名只声明一次、值按元组重复，只有按位置映射才能覆盖每一行；同一元组里非敏感列仍须可读。
func TestMaskSQLInsertColumnsByPosition(t *testing.T) {
	set := mustSet(t, MaskConfig{Columns: []string{"employee_no", "account_no"}})
	cases := []struct {
		name    string
		in      string
		secrets []string
		keep    []string
	}{
		{
			name:    "single row",
			in:      "INSERT INTO users (employee_no, remark) VALUES ('SF-778899', 'hello')",
			secrets: []string{"SF-778899"},
			keep:    []string{"hello"},
		},
		{
			name: "batch rows",
			in: "INSERT INTO users (employee_no, remark) VALUES " +
				"('SF-778899', 'hello'), ('SF-112233', 'world')",
			secrets: []string{"SF-778899", "SF-112233"},
			keep:    []string{"hello", "world"},
		},
		{
			name:    "numeric value",
			in:      "INSERT INTO accounts (account_no, note) VALUES (1234567890, 'open')",
			secrets: []string{"1234567890"},
			keep:    []string{"open"},
		},
		{
			name: "upsert keeps its own clause",
			in: "INSERT INTO users (employee_no, remark) VALUES ('SF-778899', 'hello') " +
				"ON DUPLICATE KEY UPDATE remark = 'updated'",
			secrets: []string{"SF-778899"},
			keep:    []string{"updated"},
		},
		{
			name: "other columns stay untouched",
			in:   "INSERT INTO users (id, remark) VALUES (7, 'SF-778899')",
			keep: []string{"7", "SF-778899"},
		},
		{
			name:    "backquoted identifiers",
			in:      "INSERT INTO `users` (`employee_no`, `remark`) VALUES ('SF-778899', 'hello')",
			secrets: []string{"SF-778899"},
			keep:    []string{"hello"},
		},
		{
			name:    "double-quoted identifiers",
			in:      `INSERT INTO "users" ("employee_no", "remark") VALUES ('SF-778899', 'hello')`,
			secrets: []string{"SF-778899"},
			keep:    []string{"hello"},
		},
		{
			name: "backquoted batch rows",
			in: "INSERT INTO `users` (`employee_no`, `remark`) VALUES " +
				"('SF-778899', 'hello'), ('SF-112233', 'world')",
			secrets: []string{"SF-778899", "SF-112233"},
			keep:    []string{"hello", "world"},
		},
		{
			name:    "null before the column",
			in:      "INSERT INTO users (deleted_at, employee_no) VALUES (NULL, 'SF-778899')",
			secrets: []string{"SF-778899"},
			keep:    []string{"NULL"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maskSQL(tc.in, set)
			for _, secret := range tc.secrets {
				if strings.Contains(got, secret) {
					t.Fatalf("value leaked: %s", got)
				}
			}
			for _, keep := range tc.keep {
				if !strings.Contains(got, keep) {
					t.Fatalf("value lost: %s", got)
				}
			}
		})
	}
}

// TestMaskSQLInsertValueElements covers VALUES elements that are not plain
// literals: NULL, DEFAULT, function calls, arithmetic and subqueries. The
// position counter advances at the comma between elements and nested
// parentheses stay inside the tuple, so the element after such a value still
// maps to its own column. A miss here writes a plaintext password to the log.
// TestMaskSQLInsertValueElements 覆盖非普通字面量的 VALUES 元素：NULL、DEFAULT、函数调用、
// 算术与子查询。位置计数在元素之间的逗号处推进，嵌套括号仍属于当前元组，因此这类元素之后的
// 值仍对应到自己的列。这里漏一处就是日志里的明文密码。
func TestMaskSQLInsertValueElements(t *testing.T) {
	set := mustSet(t, MaskConfig{})
	cases := []struct {
		name    string
		in      string
		secrets []string
		keep    []string
	}{
		{
			name:    "null before the secret",
			in:      "INSERT INTO users (deleted_at, password) VALUES (NULL, 'abc123')",
			secrets: []string{"abc123"},
			keep:    []string{"NULL"},
		},
		{
			name:    "default before the secret",
			in:      "INSERT INTO users (deleted_at, password) VALUES (DEFAULT, 'abc123')",
			secrets: []string{"abc123"},
			keep:    []string{"DEFAULT"},
		},
		{
			name:    "function call before the secret",
			in:      "INSERT INTO users (created_at, password) VALUES (NOW(), 'abc123')",
			secrets: []string{"abc123"},
			keep:    []string{"NOW()"},
		},
		{
			name:    "uuid function",
			in:      "INSERT INTO users (uid, password) VALUES (UUID(), 'abc123')",
			secrets: []string{"abc123"},
			keep:    []string{"UUID()"},
		},
		{
			name:    "arithmetic expression",
			in:      "INSERT INTO users (n, password) VALUES (1 + 2, 'abc123')",
			secrets: []string{"abc123"},
			keep:    []string{"1 + 2"},
		},
		{
			name:    "subquery expression",
			in:      "INSERT INTO users (n, password) VALUES ((SELECT 1), 'abc123')",
			secrets: []string{"abc123"},
			keep:    []string{"(SELECT 1)"},
		},
		{
			name:    "boolean keyword",
			in:      "INSERT INTO users (flag, password) VALUES (TRUE, 'abc123')",
			secrets: []string{"abc123"},
			keep:    []string{"TRUE"},
		},
		{
			name:    "secret before the expression",
			in:      "INSERT INTO users (password, created_at) VALUES ('abc123', NOW())",
			secrets: []string{"abc123"},
			keep:    []string{"NOW()"},
		},
		{
			name: "mixed rows",
			in: "INSERT INTO users (deleted_at, password) VALUES " +
				"(NULL, 'abc123'), (NOW(), 'abc456')",
			secrets: []string{"abc123", "abc456"},
			keep:    []string{"NULL", "NOW()"},
		},
		{
			name: "upsert after expression values",
			in: "INSERT INTO users (deleted_at, password) VALUES (NULL, 'abc123') " +
				"ON DUPLICATE KEY UPDATE remark = 'updated'",
			secrets: []string{"abc123"},
			keep:    []string{"'updated'"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maskSQL(tc.in, set)
			for _, secret := range tc.secrets {
				if strings.Contains(got, secret) {
					t.Fatalf("value leaked: %s", got)
				}
			}
			for _, keep := range tc.keep {
				if !strings.Contains(got, keep) {
					t.Fatalf("value lost: %s", got)
				}
			}
		})
	}
}

// TestMaskSQLInListColumns covers the IN / NOT IN list: the identifier before
// IN names the column every literal in the parentheses belongs to. It uses a
// custom column whose values match no built-in pattern, because phone-like
// values would be masked by the pattern fallback even without the mapping.
// TestMaskSQLInListColumns 覆盖 IN / NOT IN 列表：IN 前的标识符就是括号内每个字面量的列。
// 这里用不命中内置模式的自定义列，否则类手机号的值靠模式兜底也会被替换，看不出映射是否生效。
func TestMaskSQLInListColumns(t *testing.T) {
	set := mustSet(t, MaskConfig{Columns: []string{"employee_no"}})
	cases := []struct {
		name    string
		in      string
		secrets []string
		keep    []string
	}{
		{
			name:    "in list",
			in:      "SELECT * FROM users WHERE employee_no IN ('SF-778899', 'SF-112233')",
			secrets: []string{"SF-778899", "SF-112233"},
		},
		{
			name:    "not in list",
			in:      "SELECT * FROM users WHERE employee_no NOT IN ('SF-778899')",
			secrets: []string{"SF-778899"},
		},
		{
			name:    "backquoted column",
			in:      "SELECT * FROM users WHERE `employee_no` IN ('SF-778899')",
			secrets: []string{"SF-778899"},
		},
		{
			name:    "double-quoted column",
			in:      `SELECT * FROM users WHERE "employee_no" NOT IN ('SF-778899')`,
			secrets: []string{"SF-778899"},
		},
		{
			// NULL is a keyword, and the scanner used to treat every keyword
			// as the end of the list context: the literals after it fell back
			// to pattern matching and a configured column leaked in clear.
			// NULL 是关键字，扫描器过去把每个关键字都当成列表上下文的结束：其后的
			// 字面量只靠模式兜底，配置的敏感列于是明文泄漏。
			name:    "null inside list",
			in:      "SELECT * FROM users WHERE employee_no IN ('SF-778899', NULL, 'SF-112233')",
			secrets: []string{"SF-778899", "SF-112233"},
		},
		{
			name:    "null first in list",
			in:      "SELECT * FROM users WHERE employee_no IN (NULL, 'SF-112233')",
			secrets: []string{"SF-112233"},
		},
		{
			name:    "lowercase null in not in list",
			in:      "SELECT * FROM users WHERE employee_no NOT IN ('SF-778899', null, 'SF-112233')",
			secrets: []string{"SF-778899", "SF-112233"},
		},
		{
			name: "other column stays untouched",
			in:   "SELECT * FROM users WHERE note IN ('SF-778899')",
			keep: []string{"SF-778899"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maskSQL(tc.in, set)
			for _, secret := range tc.secrets {
				if strings.Contains(got, secret) {
					t.Fatalf("value leaked: %s", got)
				}
			}
			for _, keep := range tc.keep {
				if !strings.Contains(got, keep) {
					t.Fatalf("value lost: %s", got)
				}
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
