// Package i18n localizes user-facing text with go-i18n v2.
//
// Message files are JSON, one per language, named active.<language>.json in
// the layout go-i18n derives the language from. The bundle is immutable after
// New returns, so one instance serves every request and goroutine.
//
// Request negotiation follows the swoolefy LocaleMiddleware convention:
// the lang/locale query parameter first, then the X-Locale/X-Language
// headers, then the standard Accept-Language header, and finally the
// configured default language. Only a language with a message file can be
// selected; any other value falls through to the next source.
// 包 i18n 借助 go-i18n v2 本地化面向用户的文本。
//
// 消息文件是 JSON，每种语言一个，按 go-i18n 从文件名解析语言的约定命名为
// active.<language>.json。Bundle 在 New 返回后不可变，因此一个实例可服务所有
// 请求与 goroutine。
//
// 请求协商沿用 swoolefy LocaleMiddleware 的约定：先看 lang/locale 查询参数，再看
// X-Locale/X-Language 请求头，然后是标准的 Accept-Language，最后回落到配置的
// 默认语言。只有存在消息文件的语言才会被选中，其它值继续尝试下一个来源。
package i18n

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

var (
	// ErrFilesystemRequired is returned by New when fsys is nil and i18n is
	// enabled.
	// ErrFilesystemRequired 在启用 i18n 但 fsys 为 nil 时由 New 返回。
	ErrFilesystemRequired = errors.New("i18n: message filesystem is required")
	// ErrDefaultLanguageRequired is returned by New when DefaultLanguage is
	// empty. config.Load fills the "en" default tag, so this only fires for a
	// Config built by hand.
	// ErrDefaultLanguageRequired 在 DefaultLanguage 为空时由 New 返回。
	// config.Load 会填入 "en" 默认值，因此只有手工构造的 Config 才会触发。
	ErrDefaultLanguageRequired = errors.New("i18n: default_language is required")
	// ErrDefaultLanguageInvalid is returned when DefaultLanguage is not a
	// language tag golang.org/x/text can parse.
	// ErrDefaultLanguageInvalid 在 DefaultLanguage 不是 golang.org/x/text 能解析的
	// 语言标签时返回。
	ErrDefaultLanguageInvalid = errors.New("i18n: default_language is not a valid language tag")
	// ErrNoMessageFiles is returned when fsys contains no *.json file.
	// ErrNoMessageFiles 在 fsys 中没有任何 *.json 文件时返回。
	ErrNoMessageFiles = errors.New("i18n: no *.json message files found")
	// ErrDefaultLanguageHasNoMessages is returned when no message file is
	// named after the default language. go-i18n looks the fallback up by the
	// exact tag, so a bundle in that state would answer every lookup with a
	// not-found error instead of text.
	// ErrDefaultLanguageHasNoMessages 在没有以默认语言命名的消息文件时返回。
	// go-i18n 按完全相同的 tag 查找兜底语言，这种状态的 bundle 会让每次查找都
	// 返回未找到，而不是文本。
	ErrDefaultLanguageHasNoMessages = errors.New("i18n: no message file for default_language")
)

// Config describes the i18n section of the application configuration.
// Config 描述应用配置的 i18n 段。
type Config struct {
	// Enabled turns translation on. A missing i18n section decodes to the zero
	// value and is a normal deployment state, not a configuration error: nothing
	// is loaded, Middleware passes requests through and Translate returns message
	// IDs. Projects that do not need translations simply omit the section.
	// Enabled 控制是否启用翻译。缺少整个 i18n 段时解码得到零值，这是正常的部署状态，
	// 不是配置错误：不加载任何文件，Middleware 原样放行请求，Translate 返回消息 ID。
	// 不需要多语言的项目直接省略这一段即可。
	Enabled bool `yaml:"enabled"`

	// DefaultLanguage is the language a request falls back to when nothing
	// matches, and the language of last resort for a missing translation. It
	// must name one message file exactly — a zh-CN value does not fall back to
	// a zh file, because go-i18n resolves the fallback by exact tag. The "en"
	// default is applied by config.Load's default tag.
	// DefaultLanguage 是协商不出结果时的回落语言，也是缺失翻译时的最后兜底。它必须
	// 与某个消息文件完全对应——zh-CN 不会回落到 zh 文件，因为 go-i18n 按完全相同的
	// tag 解析兜底语言。默认值 "en" 由 config.Load 的 default tag 应用。
	DefaultLanguage string `yaml:"default_language" default:"en"`
}

// Bundle is a loaded, immutable set of message files. After New returns it is
// safe for concurrent use; nothing mutates the underlying go-i18n bundle. A
// disabled bundle has no messages: Middleware passes requests through,
// WithLanguage returns its context unchanged and Translate returns message IDs.
// Bundle 是加载完成且不可变的消息集合。New 返回后可安全并发使用，没有任何路径
// 会再修改底层 go-i18n bundle。禁用态的 bundle 没有消息：Middleware 原样放行请求，
// WithLanguage 返回传入的 context，Translate 返回消息 ID。
type Bundle struct {
	messages *goi18n.Bundle
	tags     []language.Tag
	matcher  language.Matcher
}

// New loads every *.json message file at the root of fsys and builds a bundle
// whose fallback language is Config.DefaultLanguage.
//
// A disabled Config does not fail: New returns a disabled bundle that loads
// nothing, so applications wire i18n unconditionally and a deployment without
// the section keeps working. Every other problem — a nil filesystem while
// enabled, no files, a default language without a file — is an error.
// New 加载 fsys 根目录下的所有 *.json 消息文件，并构造以 Config.DefaultLanguage
// 为回落语言的 bundle。
//
// 未启用的 Config 不算失败：New 返回禁用态的 bundle，不加载任何内容，应用因此可以
// 无条件装配 i18n，缺少配置段的部署照常运行。其余问题——启用后 fsys 为 nil、没有
// 文件、默认语言没有对应文件——仍然是错误。
//
// fsys is the locale directory itself: an embed.FS embedded with
// //go:embed *.json, or fs.Sub of a larger filesystem. Files are named
// active.<language>.json; go-i18n derives each file's language from that name.
// A default language without a matching file fails the call, because every
// translation would otherwise fall back to the message ID and a startup error
// is the only place the cause is still visible.
// fsys 就是 locale 目录本身：用 //go:embed *.json 内嵌的 embed.FS，或更大文件系统的
// fs.Sub。文件名为 active.<language>.json，go-i18n 从中解析每种文件的语言。
// 默认语言没有对应文件时直接失败：否则所有翻译都会回落到消息 ID，而启动期错误是
// 唯一还能看见原因的地方。
func New(cfg Config, fsys fs.FS) (*Bundle, error) {
	if !cfg.Enabled {
		return &Bundle{}, nil
	}
	if fsys == nil {
		return nil, ErrFilesystemRequired
	}
	raw := strings.TrimSpace(cfg.DefaultLanguage)
	if raw == "" {
		return nil, ErrDefaultLanguageRequired
	}
	defaultLanguage, err := language.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrDefaultLanguageInvalid, cfg.DefaultLanguage)
	}

	messages := goi18n.NewBundle(defaultLanguage)
	messages.RegisterUnmarshalFunc("json", json.Unmarshal)

	paths, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, fmt.Errorf("i18n: list message files: %w", err)
	}
	if len(paths) == 0 {
		return nil, ErrNoMessageFiles
	}
	loaded := make(map[language.Tag]struct{}, len(paths))
	for _, path := range paths {
		file, err := messages.LoadMessageFileFS(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("i18n: load %s: %w", path, err)
		}
		loaded[file.Tag] = struct{}{}
	}
	if _, ok := loaded[defaultLanguage]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrDefaultLanguageHasNoMessages, defaultLanguage)
	}
	tags := messages.LanguageTags()
	return &Bundle{
		messages: messages,
		tags:     tags,
		matcher:  language.NewMatcher(tags),
	}, nil
}

type localizerKey struct{}

// WithLanguage returns ctx carrying a localizer negotiated from an
// Accept-Language header (or a single language tag). It is the entry point for
// code without an HTTP request — workers, scripts, tests — and installs the
// same localizer Middleware would. A disabled bundle returns the context
// unchanged, so callers need no enabled check.
// WithLanguage 返回携带协商好 Localizer 的 ctx。参数是 Accept-Language 头（或单个
// 语言标签）。它是 worker、脚本、测试等没有 HTTP 请求的代码的入口，安装的
// Localizer 与 Middleware 一致。禁用态的 bundle 原样返回 context，调用方无需判断。
func (b *Bundle) WithLanguage(ctx context.Context, acceptLanguage string) context.Context {
	localizer := b.localizer(acceptLanguage)
	if localizer == nil {
		if ctx == nil {
			return context.Background()
		}
		return ctx
	}
	return withLocalizer(ctx, localizer)
}

// localizer builds a localizer from the candidates that name a loaded
// language, in preference order. It returns nil for a disabled bundle. Empty
// and malformed values are dropped, and so is any language without a message
// file: go-i18n's matcher would otherwise map an unsupported tag to its closest
// loaded language — fr to en, ja to zh — so "unsupported" would mean "some
// other language" instead of the configured default. With no candidate left,
// go-i18n resolves to the bundle default.
// localizer 用命中已加载语言的候选构造 Localizer，保持优先顺序；禁用态的 bundle
// 返回 nil。空值、无法解析的值和没有消息文件的语言都会被丢弃：否则 go-i18n 的
// matcher 会把不支持的 tag 映射到最接近的已加载语言（fr 到 en、ja 到 zh），
// "不支持"就变成了"另一个语言"而不是配置的默认语言。候选全部落空时，go-i18n
// 解析到 bundle 的默认语言。
func (b *Bundle) localizer(languages ...string) *goi18n.Localizer {
	if b.messages == nil {
		return nil
	}
	selected := make([]string, 0, len(languages))
	for _, value := range languages {
		selected = append(selected, b.matchLanguages(value)...)
	}
	return goi18n.NewLocalizer(b.messages, selected...)
}

// matchLanguages returns the loaded tag each preference in value resolves to.
// value is one source's raw string: a single tag or a full Accept-Language
// header, whose q-values language.ParseAcceptLanguage already sorts.
// matchLanguages 返回 value 中每个偏好解析到的已加载语言。value 是单个来源的原始
// 字符串：单个 tag 或完整的 Accept-Language 头，其中的 q 值由
// language.ParseAcceptLanguage 排好序。
func (b *Bundle) matchLanguages(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	preferences, _, err := language.ParseAcceptLanguage(value)
	if err != nil {
		return nil
	}
	matched := make([]string, 0, len(preferences))
	for _, preference := range preferences {
		if _, i, confidence := b.matcher.Match(preference); confidence != language.No {
			matched = append(matched, b.tags[i].String())
		}
	}
	return matched
}

func withLocalizer(ctx context.Context, localizer *goi18n.Localizer) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, localizerKey{}, localizer)
}

// LocalizerFrom returns the localizer installed by Middleware or WithLanguage.
// Advanced go-i18n features — plural counts, default messages, template
// functions — can use it directly; Translate covers the common cases.
// LocalizerFrom 返回 Middleware 或 WithLanguage 写入的 Localizer。需要 go-i18n
// 高级能力（复数、默认消息、模板函数）时可直接使用它；常见场景用 Translate。
func LocalizerFrom(ctx context.Context) (*goi18n.Localizer, bool) {
	if ctx == nil {
		return nil, false
	}
	localizer, ok := ctx.Value(localizerKey{}).(*goi18n.Localizer)
	return localizer, ok
}

// Translate localizes messageID, passing templateData to the message template.
//
// When ctx carries no localizer — a handler mounted without Middleware, a
// script that skipped WithLanguage — or no language defines the message, it
// returns messageID itself: the caller always gets a non-empty string, and a
// gap stays visible instead of rendering as an empty response.
// Translate 本地化 messageID，并把 templateData 交给消息模板。
//
// ctx 没有 Localizer（未安装 Middleware 的 handler、未调用 WithLanguage 的脚本）
// 或所有语言都没有该消息时返回 messageID 本身：调用方总能得到非空字符串，
// 缺失也保持可见，而不是渲染成空响应。
func Translate(ctx context.Context, messageID string, templateData any) string {
	return translate(ctx, &goi18n.LocalizeConfig{
		MessageID:    messageID,
		TemplateData: templateData,
	}, messageID)
}

// TranslatePlural is Translate for messages with plural forms: pluralCount
// selects the form (English uses "one" for 1 and "other" otherwise). When
// templateData is nil it is replaced with {"PluralCount": pluralCount}, so the
// template can read {{.PluralCount}}; a non-nil templateData must carry that
// key itself.
// TranslatePlural 是 Translate 的复数版本：pluralCount 选择复数形式（英语 1 用
// "one"，其余用 "other"）。templateData 为 nil 时会被替换成
// {"PluralCount": pluralCount}，模板因此可读取 {{.PluralCount}}；传入非 nil 的
// templateData 时需自行带上该键。
func TranslatePlural(ctx context.Context, messageID string, pluralCount any, templateData any) string {
	return translate(ctx, &goi18n.LocalizeConfig{
		MessageID:    messageID,
		PluralCount:  pluralCount,
		TemplateData: templateData,
	}, messageID)
}

func translate(ctx context.Context, cfg *goi18n.LocalizeConfig, fallback string) string {
	localizer, ok := LocalizerFrom(ctx)
	if !ok {
		return fallback
	}
	// go-i18n may return a best-effort text together with a not-found error
	// when the requested language lacks the message but the default language
	// has it; the text is what the caller wants, so the error only decides
	// when nothing was produced.
	// 请求语言缺少该消息、默认语言有时，go-i18n 会同时返回尽力结果与未找到错误；
	// 调用方要的正是这段文本，因此只有在什么都没产出时才依据错误回退。
	if text, _ := localizer.Localize(cfg); text != "" {
		return text
	}
	return fallback
}
