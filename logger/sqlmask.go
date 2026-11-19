package logger

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// MaskStrategy is how a sensitive value is rewritten before it reaches a log.
// MaskStrategy 决定敏感值写进日志前的替换方式。
type MaskStrategy string

const (
	// MaskStrategyMask keeps the shape readable: 138****8000.
	// MaskStrategyMask 保留可读形状：138****8000。
	MaskStrategyMask MaskStrategy = "mask"
	// MaskStrategyHash replaces the value with a stable digest, so the same
	// value groups together without being readable.
	// MaskStrategyHash 用稳定摘要替换，同一值分组可见但不可读。
	MaskStrategyHash MaskStrategy = "hash"
	// MaskStrategyRemove replaces the value with ***.
	// MaskStrategyRemove 用 *** 整体替换。
	MaskStrategyRemove MaskStrategy = "remove"
)

// DefaultMaskColumns is the framework's column blacklist. It lives here rather
// than in each project's config so a version upgrade can extend it: a sensitive
// column added upstream reaches every project, which is the only way a security
// baseline stays a baseline.
//
// Entries are stored in normalized form (lower case, no "_" or "-"), so
// api_key, apiKey and API-KEY all match the same entry.
// DefaultMaskColumns 是框架内置的列名黑名单。它放在框架里而不是每个项目的配置里，
// 这样升级才能把新增的敏感列带到已有项目——安全基线只有这样才能一直是基线。
//
// 表中存的是归一化形式（小写、去掉 "_" 与 "-"），因此 api_key、apiKey、API-KEY
// 命中同一条。
var DefaultMaskColumns = []string{
	"password", "passwd", "pwd", "token", "accesstoken", "refreshtoken",
	"secret", "apikey", "privatekey", "idcard", "idno", "phone", "mobile",
	"email", "bankcard", "cvv",
}

// DefaultMaskPatterns is the built-in set of value detectors. Patterns run on
// every string literal and numeric literal, so a value is masked even when its
// column name says nothing (a phone number inside a free-text WHERE clause).
// DefaultMaskPatterns 是内置的值检测器集合。它在每个字符串字面量与数字字面量上运行，
// 因此列名看不出敏感的值（比如 WHERE 里自由文本中的手机号）也会被替换。
var DefaultMaskPatterns = []string{"phone", "email", "id_card", "bank_card", "long_token"}

// builtinPatterns maps a pattern name to its detector. Names are stable API:
// config refers to them, so renaming one breaks a deployed config.
// builtinPatterns 把模式名映射到检测器。名字是对外契约：配置引用它们，改名会让已部署
// 的配置失效。
var builtinPatterns = map[string]*regexp.Regexp{
	"phone":      regexp.MustCompile(`1[3-9]\d{9}`),
	"email":      regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
	"id_card":    regexp.MustCompile(`\d{17}[\dXx]|\d{15}`),
	"bank_card":  regexp.MustCompile(`\d{16,19}`),
	"long_token": regexp.MustCompile(`[A-Za-z0-9_\-\.]{32,}`),
}

// MaskConfig is the mask section of sql_log: an independent switch, because
// "show the values" and "mask what you show" are two different decisions.
// MaskConfig 是 sql_log 的 mask 段：独立开关，因为"要不要看值"与"看了要不要替换"是
// 两个不同的决定。
type MaskConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Strategy string   `yaml:"strategy"`
	Columns  []string `yaml:"columns"`
	Patterns []string `yaml:"patterns"`
}

// maskSet is the merged, ready-to-use form: built-in tables plus whatever the
// project appended. It is built once at assembly and read-only afterwards, so
// the hot path pays no merge cost.
// maskSet 是合并后的可用形态：内置表加上项目追加的项。它在装配时构造一次，之后只读，
// 热路径因此没有合并成本。
type maskSet struct {
	strategy MaskStrategy
	columns  map[string]struct{}
	patterns []*regexp.Regexp
}

// defaultMaskSet returns the built-in tables as a usable set.
// defaultMaskSet 返回只含内置表的可用集合。
func defaultMaskSet() maskSet {
	columns := make(map[string]struct{}, len(DefaultMaskColumns))
	for _, name := range DefaultMaskColumns {
		columns[normalizeColumn(name)] = struct{}{}
	}
	patterns := make([]*regexp.Regexp, 0, len(DefaultMaskPatterns))
	for _, name := range DefaultMaskPatterns {
		patterns = append(patterns, builtinPatterns[name])
	}
	return maskSet{strategy: MaskStrategyMask, columns: columns, patterns: patterns}
}

// merge appends the project's columns and patterns to the built-in set. There
// is deliberately no way to remove a built-in entry: a security baseline that
// can be subtracted is how "temporarily commented out for one debugging session"
// becomes permanent.
// merge 把项目追加的列与模式并到内置集合上。刻意不提供删除内置项的能力：可做减法的
// 安全基线，正是"为了排查临时注释掉一行"最后变成永久配置的原因。
func (m maskSet) merge(cfg MaskConfig) (maskSet, error) {
	out := maskSet{
		strategy: m.strategy,
		columns:  make(map[string]struct{}, len(m.columns)+len(cfg.Columns)),
		patterns: append([]*regexp.Regexp(nil), m.patterns...),
	}
	for name := range m.columns {
		out.columns[name] = struct{}{}
	}

	if raw := strings.ToLower(strings.TrimSpace(cfg.Strategy)); raw != "" {
		switch MaskStrategy(raw) {
		case MaskStrategyMask, MaskStrategyHash, MaskStrategyRemove:
			out.strategy = MaskStrategy(raw)
		default:
			return m, fmt.Errorf("logger: invalid mask strategy %q (want mask, hash or remove)", cfg.Strategy)
		}
	}

	for _, column := range cfg.Columns {
		key := normalizeColumn(column)
		if key == "" {
			return m, errors.New("logger: mask column must not be empty")
		}
		out.columns[key] = struct{}{}
	}

	for _, pattern := range cfg.Patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			return m, errors.New("logger: mask pattern must not be empty")
		}
		if expr, ok := strings.CutPrefix(pattern, "re:"); ok {
			compiled, err := regexp.Compile(expr)
			if err != nil {
				return m, fmt.Errorf("logger: invalid mask pattern %q: %w", pattern, err)
			}
			out.patterns = append(out.patterns, compiled)
			continue
		}
		builtin, ok := builtinPatterns[strings.ToLower(pattern)]
		if !ok {
			return m, fmt.Errorf("logger: unknown mask pattern %q", pattern)
		}
		out.patterns = append(out.patterns, builtin)
	}
	return out, nil
}

// normalizeColumn turns a column name into its comparison key: lower case, no
// underscores or dashes. Having exactly one normalization is what makes
// "case-insensitive, snake_case or camelCase" true without matching rules per
// spelling.
// normalizeColumn 把列名变成比较键：小写、去掉下划线与连字符。只归一化一次，
// "大小写不敏感、支持下划线或驼峰"因此不需要为每种写法写一套匹配规则。
func normalizeColumn(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.Trim(name, "`\"")
	name = strings.TrimPrefix(name, "`")
	name = strings.ReplaceAll(name, "_", "")
	name = strings.ReplaceAll(name, "-", "")
	return name
}

// maskValue rewrites one sensitive value according to the strategy.
// maskValue 按策略替换一个敏感值。
func maskValue(value string, strategy MaskStrategy) string {
	if value == "" {
		return value
	}
	switch strategy {
	case MaskStrategyHash:
		sum := sha256.Sum256([]byte(value))
		return "sha256:" + hex.EncodeToString(sum[:])[:8]
	case MaskStrategyRemove:
		return "***"
	default:
		runes := []rune(value)
		// Short values would either leak or become all asterisks; the latter
		// carries no shape information, so use the fixed form.
		// 太短的值无论怎么保留都会泄漏或全是星号，后者没有形状信息，直接用固定形式。
		if len(runes) < 8 {
			return "***"
		}
		return string(runes[:3]) + strings.Repeat("*", len(runes)-7) + string(runes[len(runes)-4:])
	}
}

// maskSQL rewrites the sensitive values of an already-expanded statement. It is
// a scanner rather than a set of regular expressions: the text is interpolated
// SQL, so a literal may contain ")", ",", "=" and quotes, and the column name
// must be paired with the value that follows it.
//
// It never fails: unsupported shapes (batch INSERT column positions, for
// example) are left as they are, which is why that case is listed as a
// follow-up in the design doc instead of being claimed here.
// maskSQL 替换一条已展开语句中的敏感值。它用扫描器而不是若干正则：输入是插值后的
// SQL，字面量里可能含 ")"、","、"="" 与引号，而且必须把列名与随后的值配对。
//
// 它不会失败：不支持的形态（例如批量 INSERT 的按列位置映射）原样保留——这也是设计
// 文档把它列为后续项而不是声称已支持的原因。
func maskSQL(sql string, set maskSet) string {
	if sql == "" || (len(set.columns) == 0 && len(set.patterns) == 0) {
		return sql
	}

	var b strings.Builder
	b.Grow(len(sql))

	lastIdent := ""    // last identifier seen, normalized
	lastKeyword := ""  // last keyword seen, upper case
	inListColumn := "" // column of an IN (...) list currently open
	inListDepth := 0   // parenthesis depth of that list
	quotedIdent := ""  // last backquoted/double-quoted identifier, normalized

	isSensitive := func(value string) bool {
		column := lastIdent
		if inListColumn != "" {
			column = inListColumn
		}
		if column != "" {
			if _, ok := set.columns[column]; ok {
				return true
			}
		}
		for _, pattern := range set.patterns {
			if pattern.MatchString(value) {
				return true
			}
		}
		return false
	}

	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '\'':
			value, next := scanStringLiteral(sql, i)
			if isSensitive(value) {
				b.WriteString("'")
				b.WriteString(maskValue(value, set.strategy))
				b.WriteString("'")
			} else {
				b.WriteString(sql[i:next])
			}
			i = next
			// A literal ends the identifier slot: the next literal in
			// VALUES ('a','b') must not inherit "a"'s column.
			// 字面量结束标识符位置：VALUES ('a','b') 里的第二个字面量不能继承第一个的列名。
			lastIdent, lastKeyword, quotedIdent = "", "", ""
		case c == '`' || c == '"':
			value, next := scanQuotedIdentifier(sql, i)
			quotedIdent = normalizeColumn(value)
			lastIdent, lastKeyword = quotedIdent, ""
			b.WriteString(sql[i:next])
			i = next
		case c == '(':
			if inListDepth > 0 {
				inListDepth++
			} else if lastKeyword == "IN" && lastIdent != "" {
				inListColumn = lastIdent
				inListDepth = 1
			}
			b.WriteByte(c)
			i++
		case c == ')':
			if inListDepth > 0 {
				inListDepth--
				if inListDepth == 0 {
					inListColumn = ""
				}
			}
			b.WriteByte(c)
			i++
		case isIdentStart(c):
			start := i
			for i < len(sql) && isIdentPart(sql[i]) {
				i++
			}
			token := sql[start:i]
			if keyword := strings.ToUpper(token); keywordSet[keyword] {
				lastKeyword = keyword
				if keyword != "IN" {
					inListColumn = ""
					inListDepth = 0
				}
				lastIdent = ""
			} else {
				lastIdent, lastKeyword = normalizeColumn(token), ""
			}
			b.WriteString(token)
		case isDigit(c) || (c == '-' && i+1 < len(sql) && isDigit(sql[i+1])):
			start := i
			i++
			for i < len(sql) && (isDigit(sql[i]) || sql[i] == '.' || sql[i] == 'e' || sql[i] == 'E' || sql[i] == '+' || sql[i] == '-') {
				i++
			}
			value := sql[start:i]
			if isSensitive(value) {
				b.WriteString(maskValue(value, set.strategy))
			} else {
				b.WriteString(value)
			}
			lastIdent, lastKeyword = "", ""
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// scanStringLiteral returns the unescaped contents of the literal starting at
// start and the index just past its closing quote.
// scanStringLiteral 返回 start 处字面量的内容（已去转义），以及结束引号之后的下标。
func scanStringLiteral(sql string, start int) (string, int) {
	var b strings.Builder
	for i := start + 1; i < len(sql); i++ {
		switch sql[i] {
		case '\\':
			if i+1 < len(sql) {
				b.WriteByte(sql[i+1])
				i++
				continue
			}
			b.WriteByte(sql[i])
		case '\'':
			if i+1 < len(sql) && sql[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			return b.String(), i + 1
		default:
			b.WriteByte(sql[i])
		}
	}
	return b.String(), len(sql)
}

// scanQuotedIdentifier returns the name inside a `...` or "..." identifier and
// the index just past its closing quote.
// scanQuotedIdentifier 返回 `...` 或 "..." 标识符中的名字，以及结束引号之后的下标。
func scanQuotedIdentifier(sql string, start int) (string, int) {
	quote := sql[start]
	for i := start + 1; i < len(sql); i++ {
		if sql[i] == quote {
			if i+1 < len(sql) && sql[i+1] == quote {
				i++
				continue
			}
			return sql[start+1 : i], i + 1
		}
	}
	return sql[start+1:], len(sql)
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || isDigit(c) || c == '$'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// keywordSet keeps SQL keywords out of the identifier slot: without it "AND"
// would look like a column name and every value after it would be tested
// against the blacklist.
// keywordSet 让 SQL 关键字不进入标识符位置：否则 "AND" 会被当成列名，它后面的每个
// 值都会去查黑名单。
var keywordSet = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "AND": true, "OR": true, "NOT": true,
	"INSERT": true, "INTO": true, "VALUES": true, "UPDATE": true, "SET": true,
	"DELETE": true, "JOIN": true, "LEFT": true, "RIGHT": true, "INNER": true,
	"OUTER": true, "ON": true, "AS": true, "IN": true, "IS": true, "NULL": true,
	"LIKE": true, "BETWEEN": true, "ORDER": true, "BY": true, "GROUP": true,
	"HAVING": true, "LIMIT": true, "OFFSET": true, "UNION": true, "ALL": true,
	"EXISTS": true, "CASE": true, "WHEN": true, "THEN": true, "ELSE": true,
	"END": true, "ASC": true, "DESC": true, "DISTINCT": true, "DUPLICATE": true,
	"KEY": true, "REPLACE": true, "FOR": true, "USING": true, "TABLE": true,
}
