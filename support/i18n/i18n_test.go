package i18n_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bingcool/gofy/support/i18n"
	"github.com/gin-gonic/gin"
)

const englishMessages = `[
  {"id": "common.success", "translation": "Success"},
  {"id": "common.invalidArgument", "translation": "Invalid argument: {{.Field}}"},
  {"id": "order.items", "translation": {"one": "one item", "other": "{{.PluralCount}} items"}}
]`

const chineseMessages = `[
  {"id": "common.success", "translation": "成功"},
  {"id": "common.invalidArgument", "translation": "参数无效：{{.Field}}"},
  {"id": "common.chineseOnly", "translation": "仅中文"},
  {"id": "order.items", "translation": {"other": "{{.PluralCount}} 件商品"}}
]`

func messageFS() fstest.MapFS {
	return fstest.MapFS{
		"active.en.json": &fstest.MapFile{Data: []byte(englishMessages)},
		"active.zh.json": &fstest.MapFile{Data: []byte(chineseMessages)},
	}
}

func newBundle(t *testing.T) *i18n.Bundle {
	t.Helper()
	bundle, err := i18n.New(i18n.Config{Enabled: true, DefaultLanguage: "zh"}, messageFS())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return bundle
}

func TestNewErrors(t *testing.T) {
	valid := i18n.Config{Enabled: true, DefaultLanguage: "zh"}
	cases := []struct {
		name   string
		cfg    i18n.Config
		fsys   fs.FS
		target error
	}{
		{
			name:   "nil filesystem",
			cfg:    valid,
			target: i18n.ErrFilesystemRequired,
		},
		{
			name:   "empty default language",
			cfg:    i18n.Config{Enabled: true},
			fsys:   messageFS(),
			target: i18n.ErrDefaultLanguageRequired,
		},
		{
			name:   "invalid default language",
			cfg:    i18n.Config{Enabled: true, DefaultLanguage: "@not-a-tag"},
			fsys:   messageFS(),
			target: i18n.ErrDefaultLanguageInvalid,
		},
		{
			name:   "no message files",
			cfg:    valid,
			fsys:   fstest.MapFS{"README.md": &fstest.MapFile{Data: []byte("x")}},
			target: i18n.ErrNoMessageFiles,
		},
		{
			name:   "default language has no file",
			cfg:    i18n.Config{Enabled: true, DefaultLanguage: "fr"},
			fsys:   messageFS(),
			target: i18n.ErrDefaultLanguageHasNoMessages,
		},
		{
			name: "malformed message file",
			cfg:  valid,
			fsys: fstest.MapFS{
				"active.en.json": &fstest.MapFile{Data: []byte(`[{"id": "broken"`)},
				"active.zh.json": &fstest.MapFile{Data: []byte(chineseMessages)},
			},
			target: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := i18n.New(tc.cfg, tc.fsys)
			if tc.target == nil {
				if err == nil || !strings.Contains(err.Error(), "active.en.json") {
					t.Fatalf("want error naming the broken file, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.target) {
				t.Fatalf("want %v, got %v", tc.target, err)
			}
		})
	}
}

// A project that does not need translations omits the i18n section, which
// decodes to the zero Config. That must be a normal state, not a startup error:
// the bundle is disabled, the middleware passes requests through and Translate
// returns message IDs.
// 不需要多语言的项目直接省略 i18n 段，解码得到零值 Config。这必须是正常状态而不是
// 启动错误：bundle 处于禁用态，中间件原样放行请求，Translate 返回消息 ID。
func TestMissingSectionIsNotAnError(t *testing.T) {
	bundle, err := i18n.New(i18n.Config{}, nil)
	if err != nil {
		t.Fatalf("New(zero config) = %v, want nil", err)
	}

	ctx := bundle.WithLanguage(context.Background(), "en")
	if _, ok := i18n.LocalizerFrom(ctx); ok {
		t.Fatal("disabled bundle installed a localizer")
	}
	if got := i18n.Translate(ctx, "common.success", nil); got != "common.success" {
		t.Fatalf("Translate = %q, want the message id", got)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(i18n.Middleware(bundle))
	engine.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, i18n.Translate(c.Request.Context(), "common.success", nil))
	})
	req := httptest.NewRequest(http.MethodGet, "/?lang=en", nil)
	req.Header.Set("Accept-Language", "en")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if recorder.Body.String() != "common.success" {
		t.Fatalf("body = %q, want the message id", recorder.Body.String())
	}
}

func TestTranslate(t *testing.T) {
	bundle := newBundle(t)
	cases := []struct {
		name     string
		language string
		id       string
		data     any
		want     string
	}{
		{
			name:     "english",
			language: "en",
			id:       "common.success",
			want:     "Success",
		},
		{
			name:     "chinese default",
			language: "",
			id:       "common.success",
			want:     "成功",
		},
		{
			name:     "accept-language header",
			language: "en-US,en;q=0.9",
			id:       "common.success",
			want:     "Success",
		},
		{
			name:     "template data",
			language: "en",
			id:       "common.invalidArgument",
			data:     map[string]string{"Field": "email"},
			want:     "Invalid argument: email",
		},
		{
			name:     "unsupported language falls back to default",
			language: "fr-FR",
			id:       "common.success",
			want:     "成功",
		},
		{
			name:     "missing in requested language falls back to default file",
			language: "en",
			id:       "common.chineseOnly",
			want:     "仅中文",
		},
		{
			name:     "unknown id returns the id",
			language: "en",
			id:       "common.unknown",
			want:     "common.unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := bundle.WithLanguage(context.Background(), tc.language)
			if got := i18n.Translate(ctx, tc.id, tc.data); got != tc.want {
				t.Fatalf("Translate = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTranslateWithoutLocalizer(t *testing.T) {
	if got := i18n.Translate(context.Background(), "common.success", nil); got != "common.success" {
		t.Fatalf("Translate = %q, want the message id", got)
	}
	if got := i18n.Translate(nil, "common.success", nil); got != "common.success" {
		t.Fatalf("Translate(nil) = %q, want the message id", got)
	}
}

func TestTranslatePlural(t *testing.T) {
	bundle := newBundle(t)
	english := bundle.WithLanguage(context.Background(), "en")
	chinese := bundle.WithLanguage(context.Background(), "zh")

	if got := i18n.TranslatePlural(english, "order.items", 1, nil); got != "one item" {
		t.Fatalf("english one = %q", got)
	}
	if got := i18n.TranslatePlural(english, "order.items", 3, nil); got != "3 items" {
		t.Fatalf("english other = %q", got)
	}
	if got := i18n.TranslatePlural(chinese, "order.items", 3, nil); got != "3 件商品" {
		t.Fatalf("chinese other = %q", got)
	}
}

func TestMiddlewareNegotiation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bundle := newBundle(t)
	engine := gin.New()
	engine.Use(i18n.Middleware(bundle))
	engine.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, i18n.Translate(c.Request.Context(), "common.success", nil))
	})

	cases := []struct {
		name   string
		target string
		header map[string]string
		want   string
	}{
		{
			name: "no preference uses default",
			want: "成功",
		},
		{
			name:   "accept-language",
			header: map[string]string{"Accept-Language": "en-US,en;q=0.9"},
			want:   "Success",
		},
		{
			name:   "accept-language ranks available languages",
			header: map[string]string{"Accept-Language": "fr-FR,fr;q=0.9,en;q=0.8"},
			want:   "Success",
		},
		{
			name:   "x-locale header",
			header: map[string]string{"X-Locale": "en"},
			want:   "Success",
		},
		{
			name:   "query lang",
			target: "/?lang=en",
			want:   "Success",
		},
		{
			name:   "query locale",
			target: "/?locale=en",
			want:   "Success",
		},
		{
			name:   "query wins over headers",
			target: "/?lang=zh",
			header: map[string]string{"X-Locale": "en", "Accept-Language": "en"},
			want:   "成功",
		},
		{
			name:   "unsupported query falls through to accept-language",
			target: "/?lang=fr",
			header: map[string]string{"Accept-Language": "en"},
			want:   "Success",
		},
		{
			name:   "malformed accept-language uses default",
			header: map[string]string{"Accept-Language": "!!!"},
			want:   "成功",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.target
			if target == "" {
				target = "/"
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			for key, value := range tc.header {
				req.Header.Set(key, value)
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			if recorder.Body.String() != tc.want {
				t.Fatalf("body = %q, want %q", recorder.Body.String(), tc.want)
			}
		})
	}
}
