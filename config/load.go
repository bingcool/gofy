// Package config is an optional YAML loader. Applications may ignore it and
// load configuration however they prefer.
//
// Load decodes the whole file into a typed struct. LoadDocument returns a
// read-only view whose GetString/GetInt/GetBool methods read one key by path,
// for settings that do not deserve a struct field.
// 包 config 是一个可选的 YAML 加载器，应用可以完全不用它，按自己的方式加载配置。
// Load 把整个文件解码进有类型的结构体；LoadDocument 返回只读视图，用
// GetString/GetInt/GetBool 按路径读取单个键，适合不值得单独定义字段的设置。
package config

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/creasty/defaults"
	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

type options struct {
	expandEnv     bool
	allowMissing  bool
	envWarning    func([]string)
	strict        bool
	applyDefaults bool
	validate      bool
}

// Option customizes Load.
// Option 定制 Load 的行为。
type Option func(*options) error

// WithExpandEnv enables ${VAR} expansion (enabled by default).
// WithExpandEnv 开启 ${VAR} 展开（默认开启）。
func WithExpandEnv(enabled bool) Option {
	return func(o *options) error { o.expandEnv = enabled; return nil }
}

// WithAllowMissingEnv replaces missing ${VAR} with an empty string instead of
// failing (disabled by default). Pair it with WithEnvWarning so the substitution
// is visible rather than silent.
// WithAllowMissingEnv 把缺失的 ${VAR} 替换成空串而不是报错（默认关闭）。
// 建议配合 WithEnvWarning，让替换可见而不是静默发生。
func WithAllowMissingEnv(enabled bool) Option {
	return func(o *options) error { o.allowMissing = enabled; return nil }
}

// WithEnvWarning installs a callback notified when missing ${VAR} placeholders
// are replaced by empty strings under WithAllowMissingEnv. It receives the
// distinct variable names, once per load, so a caller can emit one warning.
// Nothing is called in strict mode: there the load fails instead. The callback
// runs on the caller's goroutine and must not be nil.
// WithEnvWarning 注册一个回调：在 WithAllowMissingEnv 下缺失的 ${VAR} 被替换成空串时，
// 回调收到去重后的变量名（每次加载一次），调用方便可输出一条警告。严格模式下不调用——
// 那属于加载失败。回调在调用方 goroutine 上运行，不能为 nil。
func WithEnvWarning(fn func([]string)) Option {
	return func(o *options) error {
		if fn == nil {
			return fmt.Errorf("config: env warning callback must not be nil")
		}
		o.envWarning = fn
		return nil
	}
}

// WithStrict rejects YAML keys that do not map to a struct field.
// WithStrict 拒绝无法映射到结构体字段的 YAML 键，用于尽早发现拼写错误。
func WithStrict(enabled bool) Option {
	return func(o *options) error { o.strict = enabled; return nil }
}

// WithApplyDefaults applies `default:"..."` tags before decoding.
// WithApplyDefaults 在解码前应用 `default:"..."` tag。
func WithApplyDefaults(enabled bool) Option {
	return func(o *options) error { o.applyDefaults = enabled; return nil }
}

// WithValidate runs the config's Validate method and validate tags.
// WithValidate 运行配置的 Validate 方法与 validate tag 校验。
func WithValidate(enabled bool) Option {
	return func(o *options) error { o.validate = enabled; return nil }
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads path into T, applying defaults, environment expansion and
// validation. It never mutates process-level state.
// Load 把 path 读入 T，依次应用默认值、环境变量展开与校验；它从不修改进程级
// 状态，因此在同一进程里加载多份配置是安全的。
func Load[T any](ctx context.Context, path string, opts ...Option) (*T, error) {
	o, data, err := readConfig(ctx, path, opts)
	if err != nil {
		return nil, err
	}

	var cfg T
	if o.applyDefaults {
		if err := defaults.Set(&cfg); err != nil {
			return nil, fmt.Errorf("config: apply defaults: %w", err)
		}
	}

	if o.strict {
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && err != io.EOF {
			return nil, fmt.Errorf("config: decode %s: %w", path, err)
		}
	} else if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: decode %s: %w", path, err)
	}

	if o.validate {
		if v, ok := any(&cfg).(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				return nil, fmt.Errorf("config: validate %s: %w", path, err)
			}
		}
		if err := validator.New().Struct(&cfg); err != nil {
			return nil, fmt.Errorf("config: validate %s: %w", path, err)
		}
	}
	return &cfg, nil
}

// readConfig resolves options and returns the file content with environment
// variables expanded. Both Load and LoadDocument share it.
// readConfig 解析选项并返回展开环境变量后的文件内容，Load 与 LoadDocument 共用；
// ctx 已取消或 path 为空时直接报错。
func readConfig(ctx context.Context, path string, opts []Option) (options, []byte, error) {
	o := options{expandEnv: true, applyDefaults: true, validate: true}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&o); err != nil {
			return options{}, nil, fmt.Errorf("config: option: %w", err)
		}
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return options{}, nil, err
		}
	}
	if strings.TrimSpace(path) == "" {
		return options{}, nil, fmt.Errorf("config: path is required")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return options{}, nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if o.expandEnv && containsPlaceholder(data) {
		expanded, err := expandEnvDocument(data, o.allowMissing, o.envWarning)
		if err != nil {
			return options{}, nil, fmt.Errorf("config: %s: %w", path, err)
		}
		data = expanded
	}
	return o, data, nil
}

// containsPlaceholder reports whether the file can contain a substitution. A
// file without one is left byte-for-byte alone, so error positions still point
// at the original text.
// containsPlaceholder 判断文件是否可能包含替换。一个都不含的文件保持原样，
// 报错位置因此仍指向原始文本。
func containsPlaceholder(data []byte) bool {
	return bytes.Contains(data, []byte("${")) || bytes.Contains(data, []byte("$$"))
}

// expandEnvDocument substitutes ${VAR} in the string scalars of an already
// parsed document.
//
// Substituting in the raw text let a value contribute YAML structure: a
// multi-line certificate, a password containing " #" or a value starting with
// "- " rewrote the document, or had its tail read as a comment, while the load
// still reported success. Rewriting scalar values only keeps every
// substitution inside the node it belongs to, and re-marshalling quotes what
// needs quoting, so the structure cannot change.
// expandEnvDocument 在已解析文档的字符串标量上替换 ${VAR}。
//
// 在原始文本上替换会让"值"参与到 YAML 结构里：多行证书、含 " #" 的口令、或以
// "- " 开头的值会改写文档，或被当成注释丢掉后半段，而加载仍然报成功。只改标量值
// 能把每次替换限制在所属节点内；重新序列化会为该加引号的值加引号，结构因此不会变。
func expandEnvDocument(data []byte, allowMissing bool, warn func([]string)) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	e := envExpander{allowMissing: allowMissing, seen: make(map[string]bool)}
	e.walk(&root)
	if len(e.missing) > 0 {
		if !allowMissing {
			return nil, fmt.Errorf("missing environment variables: %s", strings.Join(e.missing, ", "))
		}
		if warn != nil {
			warn(e.missing)
		}
	}
	if !e.changed {
		// Nothing was substituted; keep the original bytes so comments and
		// error positions survive.
		// 没有任何替换：保留原始字节，注释与报错位置都不变。
		return data, nil
	}
	out, err := yaml.Marshal(&root)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return out, nil
}

type envExpander struct {
	allowMissing bool
	changed      bool
	seen         map[string]bool
	missing      []string
}

func (e *envExpander) walk(n *yaml.Node) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode, yaml.MappingNode:
		for _, child := range n.Content {
			e.walk(child)
		}
	case yaml.ScalarNode:
		e.expand(n)
	}
	// Alias nodes share the node they anchor, which the walk already visited.
	// Alias 节点与锚点共用同一个节点，walk 已经访问过。
}

func (e *envExpander) expand(n *yaml.Node) {
	// Only strings can carry placeholders. Rewriting a number or a bool cannot
	// change it and would corrupt its tag.
	// 只有字符串可能带占位符。对数字或布尔做替换不会改变内容，却会破坏它的 tag。
	if n.Tag != "" && n.Tag != "!!str" {
		return
	}
	if !strings.Contains(n.Value, "${") && !strings.Contains(n.Value, "$$") {
		return
	}

	const dollarSentinel = "\x00gofy-dollar\x00"
	out := strings.ReplaceAll(n.Value, "$$", dollarSentinel)
	out = envPattern.ReplaceAllStringFunc(out, func(match string) string {
		name := match[2 : len(match)-1]
		value, ok := os.LookupEnv(name)
		if ok {
			return value
		}
		// Record each name once: the same variable can appear in several
		// places, and a warning listing it twice reads like two problems.
		// 同名变量只记一次：它可能出现在多处，警告里列两遍会让人以为是两个问题。
		if !e.seen[name] {
			e.seen[name] = true
			e.missing = append(e.missing, name)
		}
		if e.allowMissing {
			return ""
		}
		return match
	})
	out = strings.ReplaceAll(out, dollarSentinel, "$")
	if out != n.Value {
		e.changed = true
	}
	n.Value = out

	if n.Style != 0 {
		// A quoted or block scalar is already a string: keep that, so a
		// numeric-looking substitution cannot silently become a number.
		// 引号或块标量本身就是字符串：保持字符串语义，数字形态的替换不会悄悄变成数字。
		n.Tag = "!!str"
		return
	}
	// A plain scalar lets the substituted text decide the type again, which is
	// what the old text-level replacement did: "port: ${PORT}" still decodes
	// into an int.
	// 普通标量让替换后的文本重新决定类型，与旧的文本级替换一致：
	// "port: ${PORT}" 仍能解码成 int。
	n.Tag = ""
}
