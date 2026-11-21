package locale_test

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	gofyi18n "github.com/bingcool/gofy/support/i18n"
	"github.com/gin-gonic/gin"
	"github.com/yourorg/demo-service/app/locale"
)

type messageFile struct {
	ID string `json:"id"`
}

// messageIDs returns the IDs defined in one message file, sorted.
// messageIDs 返回一个消息文件里定义的消息 ID，已排序。
func messageIDs(t *testing.T, language string) []string {
	t.Helper()
	data, err := fs.ReadFile(locale.FS, "active."+language+".json")
	if err != nil {
		t.Fatalf("read active.%s.json: %v", language, err)
	}
	var messages []messageFile
	if err := json.Unmarshal(data, &messages); err != nil {
		t.Fatalf("parse active.%s.json: %v", language, err)
	}
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	sort.Strings(ids)
	return ids
}

// Every language must define the same message IDs. A missing translation is
// not fatal at runtime — go-i18n falls back to the default language — which is
// exactly why a gap is easy to ship unnoticed; this test is the place it fails.
// 每种语言必须定义相同的消息 ID。缺少一条翻译在运行时不是致命的——go-i18n 会回落到
// 默认语言——正因如此缺口很容易在无人察觉时发布；本测试就是让它失败的地方。
func TestMessageIDsMatchAcrossLanguages(t *testing.T) {
	english := messageIDs(t, "en")
	chinese := messageIDs(t, "zh")
	if len(english) == 0 {
		t.Fatal("no messages in active.en.json")
	}
	if len(english) != len(chinese) {
		t.Fatalf("en has %d messages, zh has %d", len(english), len(chinese))
	}
	for i := range english {
		if english[i] != chinese[i] {
			t.Fatalf("message IDs differ at %d: en %q, zh %q", i, english[i], chinese[i])
		}
	}
}

func newBundle(t *testing.T) *gofyi18n.Bundle {
	t.Helper()
	bundle, err := gofyi18n.New(gofyi18n.Config{Enabled: true, DefaultLanguage: "zh"}, locale.FS)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return bundle
}

// TestMiddlewareNegotiation drives the real message files through the real
// middleware, so the demo's JSON format, plural syntax and negotiation
// precedence are all covered by one path.
// TestMiddlewareNegotiation 用真实消息文件走真实中间件：demo 的 JSON 格式、复数
// 写法与协商优先级都由这一条路径覆盖。
func TestMiddlewareNegotiation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(gofyi18n.Middleware(newBundle(t)))
	engine.GET("/", func(c *gin.Context) {
		ctx := c.Request.Context()
		c.JSON(http.StatusOK, gin.H{
			"not_found": gofyi18n.Translate(ctx, "order.notFound", map[string]string{"OrderNo": "A-1"}),
			"items":     gofyi18n.TranslatePlural(ctx, "order.items", 2, nil),
		})
	})

	cases := []struct {
		name         string
		target       string
		header       map[string]string
		wantNotFound string
		wantItems    string
	}{
		{
			name:         "default language",
			wantNotFound: "订单 A-1 不存在",
			wantItems:    "共 2 件商品",
		},
		{
			name:         "accept-language",
			header:       map[string]string{"Accept-Language": "en-US,en;q=0.9"},
			wantNotFound: "Order A-1 was not found",
			wantItems:    "2 items",
		},
		{
			name:         "query lang",
			target:       "/?lang=en",
			wantNotFound: "Order A-1 was not found",
			wantItems:    "2 items",
		},
		{
			name:         "x-language header",
			header:       map[string]string{"X-Language": "en"},
			wantNotFound: "Order A-1 was not found",
			wantItems:    "2 items",
		},
		{
			name:         "query wins over accept-language",
			target:       "/?lang=zh",
			header:       map[string]string{"Accept-Language": "en"},
			wantNotFound: "订单 A-1 不存在",
			wantItems:    "共 2 件商品",
		},
		{
			name:         "unsupported language uses default",
			header:       map[string]string{"Accept-Language": "fr-FR"},
			wantNotFound: "订单 A-1 不存在",
			wantItems:    "共 2 件商品",
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
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var body struct {
				NotFound string `json:"not_found"`
				Items    string `json:"items"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.NotFound != tc.wantNotFound {
				t.Fatalf("order.notFound = %q, want %q", body.NotFound, tc.wantNotFound)
			}
			if body.Items != tc.wantItems {
				t.Fatalf("order.items = %q, want %q", body.Items, tc.wantItems)
			}
		})
	}
}
