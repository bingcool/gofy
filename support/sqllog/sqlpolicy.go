package sqllog

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/bingcool/gofy/support/otel"
)

// Mode selects how much of a statement reaches the log.
// Mode 决定一条语句有多少内容进入日志。
type Mode string

const (
	// ModeOff writes no statement text: failed and slow statements are still
	// reported, with the error and the timings only.
	// ModeOff 不写语句文本：失败与慢语句仍会上报，但只有错误与耗时。
	ModeOff Mode = "off"
	// ModePlaceholder writes the statement template with its placeholders.
	// ModePlaceholder 写出带占位符的语句模板。
	ModePlaceholder Mode = "placeholder"
	// ModeFull writes the expanded statement.
	// ModeFull 写出展开后的语句。
	ModeFull Mode = "full"
)

// ParseMode validates a mode name.
// ParseMode 校验档位名。
func ParseMode(name string) (Mode, error) {
	switch mode := Mode(strings.ToLower(strings.TrimSpace(name))); mode {
	case ModeOff, ModePlaceholder, ModeFull:
		return mode, nil
	default:
		return "", fmt.Errorf("sqllog: invalid sql log mode %q (want off, placeholder or full)", name)
	}
}

// Defaults applied when the corresponding field is absent or zero. They live
// here as constants so both the YAML tags and the normalization below use one
// source.
// 字段缺席或为零时使用的默认值。放在这里作为常量，使 YAML 标签与下面的归一化共用一处。
const (
	DefaultSQLSampleRate     = 0.01
	DefaultSQLMaxLength      = 4096
	DefaultSQLErrorRateLimit = 20
)

// Config is the top-level sql_log section. It is process-wide on purpose:
// masking is a compliance policy rather than a property of one connection, so
// there is exactly one place to get it right instead of one per pool.
//
// Enabled is a pointer because "absent" and "explicitly false" mean the same
// thing here but "present with other keys" does not: see ResolveEnabled.
// Config 是顶层的 sql_log 段。它刻意是进程级的：脱敏是合规策略而不是某个连接的
// 属性，所以只有一处需要配对，而不是每个连接池一处。
//
// Enabled 用指针，是因为"没写"与"显式 false"在这里含义相同，但"写了别的字段"不同：
// 见 ResolveEnabled。
type Config struct {
	Enabled *bool `yaml:"enabled"`
	// Mode: off | placeholder | full; empty means placeholder.
	//
	// There is deliberately no `default` tag here. config.Load applies
	// defaults.Set before decoding, so a tag would fill Mode even when the
	// sql_log section is absent — and then ResolveEnabled's value-based rule
	// would read that filler as "the operator configured something" and switch
	// SQL logging on for every deployment that never asked for it. normalize
	// applies the same default when the policy is built, which is the only point
	// where the value is actually needed.
	// Mode：off | placeholder | full；空串表示 placeholder。
	//
	// 这里刻意没有 `default` 标签。config.Load 在解码前先跑 defaults.Set，标签会在
	// sql_log 整段缺席时也把 Mode 填上——随后 ResolveEnabled 的按值规则会把这份填充读成
	// "运维配置了东西"，从而给每一个从未要求过 SQL 日志的部署打开日志。normalize 在构造
	// 策略时会填同一个默认值，那才是真正需要这个值的地方。
	Mode string `yaml:"mode"`
	// SampleRate applies to Info rows; 0 means "not set" (use the default), 1
	// means every row. 0 used to mean "log everything", which contradicted the
	// zero value of a missing field, so that reading was dropped.
	// SampleRate 作用于 Info 行；0 表示"未设置"（用默认值），1 表示全部。原先 0 与 1
	// 都表示全打，但 0 正是字段缺席时的零值，那个含义会与"没写"冲突，因此取消。
	SampleRate float64 `yaml:"sample_rate"`
	// MaxLength truncates one statement; -1 disables truncation.
	// MaxLength 截断单条语句；-1 表示不截断。
	MaxLength int `yaml:"max_length"`
	// LogSchemaQueries keeps information_schema queries. The field is phrased
	// positively so its zero value (false) is also the default (filter them):
	// with an "ignore_..." name the default would be true, which a bool cannot
	// express without a pointer.
	// LogSchemaQueries 保留 information_schema 查询。字段名是正向的，因此零值（false）
	// 就是默认值（过滤掉）：若叫 "ignore_..."，默认值就是 true，而 bool 若不借助指针
	// 无法区分"没写"与"写了 false"。
	LogSchemaQueries bool `yaml:"log_schema_queries"`
	// ErrorRateLimit caps failed statements per second; -1 disables the cap.
	// ErrorRateLimit 限制每秒失败语句条数；-1 表示不限。
	ErrorRateLimit int `yaml:"error_rate_limit"`
	// SlowRateLimit caps slow statements per second; 0 or -1 disables the cap.
	// SlowRateLimit 限制每秒慢语句条数；0 或 -1 表示不限。
	SlowRateLimit int        `yaml:"slow_rate_limit"`
	Mask          MaskConfig `yaml:"mask"`

	// keys records which YAML keys appeared in this section, so "wrote
	// anything" can be told apart from "wrote an explicit zero value".
	// keys 记录本节出现过哪些 YAML 键，用来区分"写了东西"与"写了零值"。
	keys map[string]struct{}
}

// UnmarshalYAML records the keys present in the section before decoding. A
// value-based check cannot do this: sql_log: {log_schema_queries: false} has no
// non-zero field, yet the operator clearly meant to configure the feature.
// UnmarshalYAML 在解码前记录本节出现过的键。按值判断做不到：`sql_log:
// {log_schema_queries: false}` 没有任何非零字段，但写配置的人显然是想启用这个功能。
func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.MappingNode {
		keys := make(map[string]struct{}, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			keys[node.Content[i].Value] = struct{}{}
		}
		c.keys = keys
	}
	type plain Config
	return node.Decode((*plain)(c))
}

// ResolveEnabled reports whether the section is active. Three rules, in order:
//
//   - an explicit enabled wins;
//   - otherwise any other key in the YAML section means "enabled" — writing mode:
//     full and forgetting enabled must not silently do nothing;
//   - otherwise any non-zero field means "enabled", so a Config built in code
//     behaves exactly like the YAML that would produce it.
//
// The third rule is what makes the two entry paths agree. keys only exists on
// the YAML path, so a programmatic Config{Mode: "full"} would otherwise resolve
// to disabled while sql_log: {mode: full} resolved to enabled — the same
// configuration, two behaviours depending on how it was loaded. The YAML rule
// still comes first because it can see something values cannot: an explicit
// zero, such as log_schema_queries: false.
// ResolveEnabled 报告本节是否生效。三条规则，按顺序：
//
//   - 显式 enabled 优先；
//   - 否则只要 YAML 里出现了其它键就算启用——写了 mode: full 却忘了 enabled，不能静默失效；
//   - 否则只要有任一非零字段就算启用，使代码里构造的 Config 与产生它的 YAML 行为一致。
//
// 第三条是两条入口路径达成一致的关键。keys 只存在于 YAML 路径上，因此程序化构造的
// Config{Mode: "full"} 原先解析为关闭，而 sql_log: {mode: full} 解析为启用——同一份配置，
// 两种行为，只取决于怎么加载。YAML 规则仍然排在前面，因为它能看到值看不到的东西：显式
// 零值，例如 log_schema_queries: false。
func (c Config) ResolveEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	for key := range c.keys {
		if key != "enabled" {
			return true
		}
	}
	return c.configured()
}

// configured reports whether any field was given a value. It is the value-based
// counterpart of the YAML key scan, used when the struct was built in code.
//
// A field left at its zero value is indistinguishable from a field that was
// never mentioned, which is a property of Go structs, not of this function:
// Config{LogSchemaQueries: false} states no intent either way. Callers that mean
// "off" say so through Enabled.
// configured 报告是否有任一字段被赋值。它是 YAML 键扫描在值层面的对应物，用于在代码里
// 构造结构体的情形。
//
// 保持零值的字段与从未提及的字段无法区分，这是 Go 结构体的性质而不是本函数的：
// Config{LogSchemaQueries: false} 没有表达任何意图。想表达"关闭"的调用方用 Enabled。
func (c Config) configured() bool {
	if c.Mode != "" || c.SampleRate != 0 || c.MaxLength != 0 ||
		c.LogSchemaQueries || c.ErrorRateLimit != 0 || c.SlowRateLimit != 0 {
		return true
	}
	m := c.Mask
	return m.Enabled || m.Strategy != "" || len(m.Columns) > 0 || len(m.Patterns) > 0
}

// normalize fills defaults and validates the values. It runs on every entry
// path, not only under config.Load: a caller that decodes YAML itself or builds
// the struct in code must see the same behaviour as one that uses the loader.
// normalize 填默认值并校验。它在每条入口路径上运行，而不只是 config.Load 之后：
// 自己解码 YAML 或在代码里构造结构体的调用方，必须与走加载器的调用方得到相同行为。
func (c Config) normalize() (Config, error) {
	out := c
	out.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if out.Mode == "" {
		out.Mode = string(ModePlaceholder)
	}
	if _, err := ParseMode(out.Mode); err != nil {
		return c, err
	}
	if out.SampleRate == 0 {
		out.SampleRate = DefaultSQLSampleRate
	}
	if out.SampleRate < 0 || out.SampleRate > 1 {
		return c, fmt.Errorf("sqllog: sql log sample rate %v must be within [0,1]", c.SampleRate)
	}
	if out.MaxLength == 0 {
		out.MaxLength = DefaultSQLMaxLength
	}
	if out.MaxLength < -1 {
		return c, fmt.Errorf("sqllog: sql log max length %d must be -1 or not negative", c.MaxLength)
	}
	if out.ErrorRateLimit == 0 {
		out.ErrorRateLimit = DefaultSQLErrorRateLimit
	}
	if out.ErrorRateLimit < -1 {
		return c, fmt.Errorf("sqllog: sql log error rate limit %d must be -1 or not negative", c.ErrorRateLimit)
	}
	if out.SlowRateLimit < -1 {
		return c, fmt.Errorf("sqllog: sql log slow rate limit %d must be -1 or not negative", c.SlowRateLimit)
	}
	if out.SlowRateLimit < 0 {
		out.SlowRateLimit = 0
	}
	return out, nil
}

// Policy is the process-wide SQL log policy: one sampler and one pair of
// limiters shared by every connection. Sharing is the point — error_rate_limit
// means "this process writes at most N per second", and per-connection counters
// would multiply that by the number of pools.
// Policy 是进程级 SQL 日志策略：一个采样器、一对限流器，被所有连接共享。共享是关键——
// error_rate_limit 的含义是"本进程每秒最多写 N 条"，每个连接各持一份计数器会把这个数
// 乘以连接池数量。
type Policy struct {
	cfg          Config
	mask         maskSet
	sampler      *sampler
	errorLimiter *limiter
	slowLimiter  *limiter
	disabled     bool
}

// NewPolicy builds the policy. A disabled section returns a disabled policy
// without validating or compiling anything: the values do not take effect, so a
// stale value left in a switched-off section must not stop the process.
// NewPolicy 构造策略。关闭的段落返回一个关闭的策略，不校验也不编译任何东西：
// 值本来就不生效，关掉的配置里留着一个旧值不该让进程起不来。
func NewPolicy(cfg Config) (*Policy, error) {
	if !cfg.ResolveEnabled() {
		return &Policy{disabled: true}, nil
	}
	normalized, err := cfg.normalize()
	if err != nil {
		return nil, err
	}
	set, err := defaultMaskSet().merge(cfg.Mask)
	if err != nil {
		return nil, err
	}
	return &Policy{
		cfg:          normalized,
		mask:         set,
		sampler:      &sampler{rate: normalized.SampleRate},
		errorLimiter: &limiter{limit: normalized.ErrorRateLimit},
		slowLimiter:  &limiter{limit: normalized.SlowRateLimit},
	}, nil
}

// Disabled reports whether the whole sql_log layer is switched off. A disabled
// policy produces no entries at all, including failed statements.
// Disabled 报告整个 SQL 日志层是否关闭。关闭的策略不产生任何条目，失败语句也没有。
func (p *Policy) Disabled() bool { return p == nil || p.disabled }

// Mode returns the statement-text mode.
// Mode 返回语句文本档位。
func (p *Policy) Mode() Mode {
	if p == nil || p.disabled {
		return ModeOff
	}
	return Mode(p.cfg.Mode)
}

// MaskEnabled reports whether the mask section is on.
// MaskEnabled 报告脱敏开关是否打开。
func (p *Policy) MaskEnabled() bool { return p != nil && !p.disabled && p.cfg.Mask.Enabled }

// MaxLength returns the truncation limit; -1 means no truncation.
// MaxLength 返回截断上限；-1 表示不截断。
func (p *Policy) MaxLength() int {
	if p == nil || p.disabled {
		return -1
	}
	return p.cfg.MaxLength
}

// LogSchemaQueries reports whether information_schema queries are kept.
// LogSchemaQueries 报告是否保留 information_schema 查询。
func (p *Policy) LogSchemaQueries() bool { return p != nil && !p.disabled && p.cfg.LogSchemaQueries }

// SampleInfo reports whether an informational entry should be written.
// SampleInfo 报告一条 Info 级条目是否应写出。
func (p *Policy) SampleInfo() bool {
	if p == nil || p.disabled {
		return false
	}
	return p.sampler.allow()
}

// AllowError reports whether a failed statement may be written, and how many
// were suppressed in the window that just closed.
// AllowError 报告失败语句可否写出，以及刚刚结束的窗口里压制了多少条。
func (p *Policy) AllowError() (bool, int) {
	if p == nil {
		return true, 0
	}
	return p.errorLimiter.allow()
}

// AllowSlow reports whether a slow statement may be written, and how many were
// suppressed in the window that just closed.
// AllowSlow 报告慢语句可否写出，以及刚刚结束的窗口里压制了多少条。
func (p *Policy) AllowSlow() (bool, int) {
	if p == nil {
		return true, 0
	}
	return p.slowLimiter.allow()
}

// MaskSQL applies the mask set; it is a no-op when masking is off.
// MaskSQL 应用脱敏集合；脱敏关闭时原样返回。
func (p *Policy) MaskSQL(sql string) string {
	if !p.MaskEnabled() {
		return sql
	}
	return maskSQL(sql, p.mask)
}

// Truncate clamps the statement and reports whether it was cut.
// Truncate 截断语句，并报告是否发生截断。
func (p *Policy) Truncate(sql string) (string, bool) {
	return truncateLimit(sql, p.MaxLength())
}

// sqlTraceFields returns the trace ids carried by ctx. The two id systems stay
// separate: X-Trace-Id is the cross-service business id, trace_id is the OTel
// span. Merging them would make a log line impossible to attribute.
// sqlTraceFields 返回 ctx 携带的链路 id。两套 id 保持分开：X-Trace-Id 是跨服务的业务
// id，trace_id 是 OTel Span。合并后一条日志就无法归属到确定的链路。
func sqlTraceFields(ctx context.Context) []zap.Field {
	if ctx == nil {
		return nil
	}
	var fields []zap.Field
	if id := headerpropagation.HeaderFromContext(ctx, headerpropagation.HeaderTraceID); id != "" {
		fields = append(fields, zap.String("x_trace_id", id))
	}
	return append(fields, otel.TraceFields(ctx)...)
}
