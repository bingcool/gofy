# support/i18n

基于 [go-i18n v2](https://github.com/nicksnyder/go-i18n) 的多语言文案包：加载
`active.<语言>.json` 消息文件、按请求协商语言，并在 handler 里用
`i18n.Translate(ctx, id, data)` 取文案。协商顺序对齐 swoolefy 的
`LocaleMiddleware`。

包只依赖 go-i18n、`golang.org/x/text` 与 gin；`*Bundle` 在 `New` 返回后不可变，
可并发使用。

## 快速开始

```go
import (
    "embed"

    gofyi18n "github.com/bingcool/gofy/support/i18n"
)

// 消息文件随二进制内嵌；FS 指向 locale 目录本身，不是它的上级目录
//go:embed *.json
var localeFS embed.FS

bundle, err := gofyi18n.New(gofyi18n.Config{
    Enabled:         true,
    DefaultLanguage: "zh",
}, localeFS)
if err != nil {
    return err // 默认语言没有消息文件、文件解析失败等都在启动期暴露
}

app := gin.New()
app.Use(gofyi18n.Middleware(bundle))
app.GET("/orders/:no", func(c *gin.Context) {
    // 中间件已把 Localizer 写进 c.Request.Context()
    c.String(200, gofyi18n.Translate(c.Request.Context(), "order.notFound",
        map[string]string{"OrderNo": c.Param("no")}))
})
```

`Translate` 的返回值始终非空：没有安装中间件、或所有语言都缺少该消息时返回
消息 ID 本身。缺失因此可见，而不是渲染成空串。

## 消息文件

文件名必须是 `active.<语言>.json`，go-i18n 从文件名解析语言标签；加载不递归，
`FS` 要指向这些文件所在的目录。复数写成对象：

```json
[
  {"id": "common.success", "translation": "成功"},
  {"id": "order.notFound", "translation": "订单 {{.OrderNo}} 不存在"},
  {"id": "order.items", "translation": {"other": "共 {{.PluralCount}} 件商品"}}
]
```

英语等多复数形式的语言写 `{"one": "...", "other": "..."}`，中文、日文只有
`other`。`TranslatePlural(ctx, id, count, data)` 会按语言规则选形式；`data` 为
nil 时自动注入 `{"PluralCount": count}`，模板里的 `{{.PluralCount}}` 因此可直接
使用，传入 `data` 时则需自行带上该键。

## 协商顺序

优先级从高到低：

1. Query：`lang` / `locale`
2. Header：`X-Locale` / `X-Language`
3. Header：`Accept-Language`（按 q 值排序）
4. 配置的 `default_language`

**只有已加载的语言会被选中。** 不支持的候选（如只加载了 en/zh 时的 `fr`）会被
忽略并继续看下一个来源，全部落空才用默认语言。这一点是刻意的：go-i18n 的
matcher 对不认识的标签会返回"最接近"的已加载语言（fr → en、ja → zh），不拦截
的话"不支持"就变成了"另一个语言"。

## 配置与启动失败

`Config` 对齐 `config.Load`（含 `default:"en"` tag），YAML 段：

```yaml
i18n:
  enabled: true
  default_language: zh
```

**整段可以不配置。** 缺少 `i18n` 段时解码得到零值，这就是"不启用"：`New`
返回一个禁用态的 bundle，不报错也不加载文件；`Middleware` 原样放行请求，
`Translate` 返回消息 ID，`WithLanguage` 原样返回 context。因此应用可以无条件装配
i18n，不需要多语言的项目直接省略这一段即可。

启用之后，配置错误仍然让启动失败：

| 情况 | 结果 |
| --- | --- |
| `enabled: false` 或整段缺失 | 禁用态 bundle，无错误 |
| `enabled: true` 且 `fsys` 为 nil | `ErrFilesystemRequired` |
| `default_language` 为空 / 无法解析 | `ErrDefaultLanguageRequired` / `ErrDefaultLanguageInvalid` |
| 目录里没有 `*.json` | `ErrNoMessageFiles` |
| 默认语言没有对应文件 | `ErrDefaultLanguageHasNoMessages` |
| 某个文件 JSON 或复数格式错误 | 包装错误，带文件名 |

最后一条值得强调：go-i18n 的兜底语言按**完全相同的 tag** 查找，`zh-CN` 不会
回落到 `zh` 文件；默认语言没有文件时每次翻译都只得到消息 ID。这种状态在运行期
很难发现，所以 `New` 直接让启动失败。

## 非 HTTP 场景

worker、脚本、测试没有请求头可协商，用 `WithLanguage` 把同一套 Localizer 装进
context：

```go
ctx = bundle.WithLanguage(ctx, "en")             // 单个标签
ctx = bundle.WithLanguage(ctx, "en-US,en;q=0.9") // Accept-Language 头也可以
i18n.Translate(ctx, "common.success", nil)
```

需要 go-i18n 的高级能力（默认消息、模板函数、`LocalizeWithTag`）时，用
`LocalizerFrom(ctx)` 取出原始 `*i18n.Localizer`。

## demo 的用法

`examples/demo-service/app/locale` 内嵌 `active.en.json` / `active.zh.json`，
`options.go` 无条件构造 bundle 并安装中间件；`i18n.enabled` 为 false 或整段缺失时
bundle 处于禁用态，中间件原样放行。消息文件随二进制编译，改文案要重新构建；
配置热更新只重建模块，不会重新读取它们。

## 与 PHP 的差异

- 消息文件用 go-i18n v2 的 JSON 格式（ID + translation 对象），不读 PHP 的
  `Config/i18n.php`。
- 不内置 cookie 协商；PHP 的 `LocaleMiddleware` 主要来源是 query / header，
  需要 cookie 时可在中间件之前自行读取并改写请求，或在业务层自行协商。
- 只做协商、不需要翻译时不必引入本包。