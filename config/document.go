package config

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	maxInt   = int(^uint(0) >> 1)
	maxInt64 = int64(^uint64(0) >> 1)
)

// Document is a read-only view of a YAML file that reads single values by key
// path. It needs no predeclared struct, so it fits scattered settings or the
// configuration of third-party modules.
// Paths are dot separated: maps are looked up by key and sequences by numeric
// index, for example "db.default.dsn" or "servers.0.host".
// Document 是 YAML 配置的只读视图，按键路径读取单个值。它不要求预先定义
// 结构体，适合读取零散设置或第三方模块的配置。
//
// 路径用点号分隔，映射按键查找，序列按数字下标查找，例如
// "db.default.dsn" 或 "servers.0.host"。
type Document struct {
	data map[string]any
}

// LoadDocument reads path and returns a Document readable by key path. Like Load
// it expands ${VAR}; only WithExpandEnv and WithAllowMissingEnv apply, because
// WithStrict, WithApplyDefaults and WithValidate are meant for struct decoding.
// LoadDocument 读取 path 并返回可按键路径读取的 Document。
// 与 Load 一样会展开 ${VAR}；仅 WithExpandEnv 和 WithAllowMissingEnv 生效，
// WithStrict、WithApplyDefaults、WithValidate 面向结构体解码，对 Document 无意义。
func LoadDocument(ctx context.Context, path string, opts ...Option) (*Document, error) {
	_, data, err := readConfig(ctx, path, opts)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config: decode %s: %w", path, err)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	return &Document{data: raw}, nil
}

// Get returns the raw value at path. A key present with a null value returns
// (nil, true); a missing path, or an intermediate node that is neither a map nor a
// sequence, returns (nil, false).
// Get 返回路径对应的原始值。键存在但值为 null 时返回 (nil, true)；
// 路径不存在或中间节点不是映射/序列时返回 (nil, false)。
func (d *Document) Get(path string) (any, bool) {
	if d == nil {
		return nil, false
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false
	}

	var current any = d.data
	for _, segment := range strings.Split(path, ".") {
		if segment == "" {
			return nil, false
		}
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[segment]
			if !ok {
				return nil, false
			}
			current = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// Has reports whether the path exists.
// Has 判断路径是否存在。
func (d *Document) Has(path string) bool {
	_, ok := d.Get(path)
	return ok
}

// GetString reads a string. YAML scalars (strings, numbers, booleans) are converted
// to text; maps, sequences and null return false.
// GetString 读取字符串。YAML 的标量（字符串、数字、布尔）都会转成文本，
// 映射、序列和 null 返回 false。
func (d *Document) GetString(path string) (string, bool) {
	value, ok := d.Get(path)
	if !ok {
		return "", false
	}
	return scalarString(value)
}

// GetInt reads an integer. YAML integers and strings parseable by strconv.Atoi are
// accepted; values out of int range, decimals and other types return false.
// GetInt 读取整数。接受 YAML 整数和可被 strconv.Atoi 解析的字符串；
// 超出 int 范围、带小数或其它类型返回 false。
func (d *Document) GetInt(path string) (int, bool) {
	value, ok := d.Get(path)
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		if int64(int(v)) != v {
			return 0, false
		}
		return int(v), true
	case uint64:
		if v > uint64(maxInt) {
			return 0, false
		}
		return int(v), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// GetInt64 reads a 64-bit integer with the same rules as GetInt.
// GetInt64 读取 64 位整数，规则与 GetInt 相同。
func (d *Document) GetInt64(path string) (int64, bool) {
	value, ok := d.Get(path)
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case uint64:
		if v > uint64(maxInt64) {
			return 0, false
		}
		return int64(v), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// GetFloat64 reads a float. YAML numbers and strings parseable by
// strconv.ParseFloat are accepted.
// GetFloat64 读取浮点数。接受 YAML 数字和可被 strconv.ParseFloat 解析的字符串。
func (d *Document) GetFloat64(path string) (float64, bool) {
	value, ok := d.Get(path)
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint64:
		return float64(v), true
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// GetBool reads a boolean. YAML booleans and the strings strconv.ParseBool accepts
// (true/false, 1/0, t/f, ...) are accepted.
// GetBool 读取布尔值。接受 YAML 布尔和 strconv.ParseBool 支持的字符串
// （true/false、1/0、t/f 等）。
func (d *Document) GetBool(path string) (bool, bool) {
	value, ok := d.Get(path)
	if !ok {
		return false, false
	}
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return false, false
		}
		return b, true
	default:
		return false, false
	}
}

// GetDuration reads a duration and only accepts strings understood by
// time.ParseDuration (such as 1h, 30s, 200ms). A bare number is ambiguous (seconds or
// nanoseconds), so it returns false; use GetInt/GetInt64 for numbers.
// GetDuration 读取时长，只接受 time.ParseDuration 支持的字符串（如 1h、30s、200ms）。
// 裸数字含义不明确（秒还是纳秒），因此返回 false，需要时请用 GetInt/GetInt64。
func (d *Document) GetDuration(path string) (time.Duration, bool) {
	value, ok := d.Get(path)
	if !ok {
		return 0, false
	}
	text, ok := value.(string)
	if !ok {
		return 0, false
	}
	duration, err := time.ParseDuration(strings.TrimSpace(text))
	if err != nil {
		return 0, false
	}
	return duration, true
}

// GetStringSlice reads a sequence of strings, converting elements by the GetString
// rules; a map or sequence inside the sequence returns false.
// GetStringSlice 读取字符串序列，元素按 GetString 的规则转换；
// 序列中出现映射或序列时返回 false。
func (d *Document) GetStringSlice(path string) ([]string, bool) {
	value, ok := d.Get(path)
	if !ok {
		return nil, false
	}
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...), true
	case []any:
		items := make([]string, 0, len(v))
		for _, item := range v {
			text, ok := scalarString(item)
			if !ok {
				return nil, false
			}
			items = append(items, text)
		}
		return items, true
	default:
		return nil, false
	}
}

func scalarString(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case int:
		return strconv.Itoa(v), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), true
	default:
		return "", false
	}
}
