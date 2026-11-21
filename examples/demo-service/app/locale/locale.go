// Package locale embeds the demo's message files. Each file is named
// active.<language>.json — the layout support/i18n loads — and i18n.New derives
// the language from the file name.
// Package locale 内嵌 demo 的消息文件。每个文件名为 active.<language>.json，
// 即 support/i18n 加载的布局；i18n.New 从文件名解析语言。
package locale

import "embed"

// FS is the locale directory itself, which is exactly what i18n.New expects.
// Pointing it at a parent directory would make startup fail with "no message
// files", because loading is not recursive.
// FS 就是 locale 目录本身，正是 i18n.New 期望的输入。指向上级目录会让启动以
// "no message files" 失败，因为加载不会递归。
//
//go:embed *.json
var FS embed.FS
