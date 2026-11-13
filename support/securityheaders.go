package support

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// SecurityHeadersConfig sets response headers that harden browser behavior.
// Zero values select the defaults documented on each field.
// SecurityHeadersConfig 设置用于加固浏览器行为的响应头；零值表示使用各字段
// 注释中的默认值，空指针字段则不写该响应头。
type SecurityHeadersConfig struct {
	// ContentTypeOptions is normally "nosniff".
	// ContentTypeOptions 通常为 "nosniff"，阻止浏览器猜测 Content-Type。
	ContentTypeOptions string
	// FrameOptions is normally "DENY".
	// FrameOptions 通常为 "DENY"，禁止页面被嵌入 iframe。
	FrameOptions string
	// ReferrerPolicy is normally "strict-origin-when-cross-origin".
	// ReferrerPolicy 通常为 "strict-origin-when-cross-origin"，控制跨域时的
	// Referer 泄露范围。
	ReferrerPolicy string
	// HSTS enables Strict-Transport-Security for this duration. Zero disables
	// it; only enable it when the service is served over HTTPS.
	// HSTS 按该时长启用 Strict-Transport-Security，零值表示不启用；仅在服务
	// 通过 HTTPS 提供时开启，否则会让客户端无法回退到 HTTP。
	HSTS time.Duration
	// HSTSIncludeSubdomains adds includeSubDomains.
	// HSTSIncludeSubdomains 追加 includeSubDomains，子域同样受 HSTS 约束。
	HSTSIncludeSubdomains bool
	// HSTSPreload adds preload.
	// HSTSPreload 追加 preload，供浏览器内置列表收录；启用后很难撤销。
	HSTSPreload bool
	// ContentSecurityPolicy is emitted when non-empty.
	// ContentSecurityPolicy 非空时才写出，取值需自行保证策略正确。
	ContentSecurityPolicy string
	// PermissionsPolicy is emitted when non-empty.
	// PermissionsPolicy 非空时才写出，用于禁用不需要的浏览器特性。
	PermissionsPolicy string
	// Custom headers are applied last and may override the defaults above.
	// Custom 最后写入，可覆盖上面的默认头；名称与取值都会经过 Validate 检查。
	Custom map[string]string
}

// DefaultSecurityHeadersConfig returns the headers gofy applies when
// WithSecurityHeaders is given a zero config.
// DefaultSecurityHeadersConfig 返回 WithSecurityHeaders 收到零值配置时 gofy 实际
// 应用的响应头，便于调用方在此基础上增补字段。
func DefaultSecurityHeadersConfig() SecurityHeadersConfig {
	return SecurityHeadersConfig{
		ContentTypeOptions: "nosniff",
		FrameOptions:       "DENY",
		ReferrerPolicy:     "strict-origin-when-cross-origin",
	}
}

// Validate rejects values that would inject extra headers.
// Validate 拒绝可能注入额外响应头的取值：取值含 CR/LF、头名为空或含非法字符，
// 以及负的 HSTS 时长。
func (c SecurityHeadersConfig) Validate() error {
	values := []string{
		c.ContentTypeOptions, c.FrameOptions, c.ReferrerPolicy,
		c.ContentSecurityPolicy, c.PermissionsPolicy,
	}
	for _, v := range values {
		if strings.ContainsAny(v, "\r\n") {
			return errors.New("support: security header value must not contain newlines")
		}
	}
	if c.HSTS < 0 {
		return errors.New("support: hsts duration must not be negative")
	}
	for name, value := range c.Custom {
		if strings.TrimSpace(name) == "" {
			return errors.New("support: security header name must not be empty")
		}
		if strings.ContainsAny(name, "\r\n:") {
			return fmt.Errorf("support: invalid security header name %q", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("support: security header %q value must not contain newlines", name)
		}
	}
	return nil
}
