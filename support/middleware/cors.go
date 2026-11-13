package middleware

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
)

// NameCORS is the middleware alias CORS uses on contract routes.
// NameCORS 是 CORS 在契约路由里使用的中间件别名。
const NameCORS = "cors"

// CORS builds the cross-origin middleware, matching swoolefy CorsMiddleware:
//
//   - AllowOrigins accepts bare domains (http/https expanded automatically) and
//     full origins;
//   - AllowOriginPatterns accepts *.example.com subdomain matching;
//   - AllowPaths is a RequestURI allowlist including the query, with shell
//     wildcards;
//   - a request carrying Origin is checked against origin and path first, and
//     rejected with 403 when it does not match;
//   - once it matches, the full CORS header set is written for any method, and
//     OPTIONS always answers 204.
//
// When both AllowOrigins and AllowOriginPatterns are empty, CORS is disabled and
// (nil, nil) is returned.
// CORS 构建跨域中间件，行为对齐 swoolefy CorsMiddleware：
//
//   - AllowOrigins 支持裸域名（自动展开 http/https）与完整来源；
//   - AllowOriginPatterns 支持 *.example.com 子域匹配；
//   - AllowPaths 是含 query 的 RequestURI 白名单，支持 shell 通配符；
//   - 带 Origin 的请求先校验来源与路径，不匹配直接 403；
//   - 命中后无论什么方法都写全套 CORS 响应头，OPTIONS 统一回 204。
//
// AllowOrigins 与 AllowOriginPatterns 都为空时不启用 CORS，返回 (nil, nil)。
func CORS(cfg support.CORSConfig) (gin.HandlerFunc, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(cfg.AllowOrigins) == 0 && len(cfg.AllowOriginPatterns) == 0 {
		return nil, nil
	}
	policy, err := newCORSPolicy(cfg)
	if err != nil {
		return nil, err
	}
	return policy.handle, nil
}

// corsPolicy is the read-only policy resolved at startup: origin sets, pattern
// regexps, path regexps and header values are all computed while constructing,
// so request handling only matches and writes headers.
// corsPolicy 是启动时算好的只读策略：来源集合、模式正则、路径正则和响应头
// 取值都在构造阶段完成，请求期只做匹配与写头。
type corsPolicy struct {
	allowAll    bool                // AllowOrigins 是否包含 "*"
	origins     map[string]struct{} // 精确来源集合（裸域名已展开 http/https）
	patterns    []*regexp.Regexp    // subdomain pattern regexps; 子域模式正则
	paths       []*regexp.Regexp    // 路径白名单正则（fnmatch 风格）
	methods     string              // Access-Control-Allow-Methods 值
	headers     string              // Access-Control-Allow-Headers 值
	expose      string              // Access-Control-Expose-Headers 值
	credentials bool                // whether credentials are allowed; 是否允许携带凭证
	maxAge      int                 // Access-Control-Max-Age 秒数
}

func newCORSPolicy(cfg support.CORSConfig) (*corsPolicy, error) {
	policy := &corsPolicy{
		origins:     make(map[string]struct{}, len(cfg.AllowOrigins)*2),
		credentials: cfg.AllowCredentials,
	}

	// Expand exact origins: a bare domain allows both http and https, as PHP
	// does; an origin that already carries a scheme is matched verbatim.
	// 展开精确来源：裸域名同时放行 http 与 https，与 PHP 一致；
	// 已带 scheme 的来源按原样精确匹配。
	for _, raw := range cfg.AllowOrigins {
		origin := strings.Trim(strings.TrimSpace(raw), "/")
		if origin == "" {
			continue
		}
		if origin == "*" {
			policy.allowAll = true
			continue
		}
		if hasOriginScheme(origin) {
			policy.origins[origin] = struct{}{}
			continue
		}
		policy.origins["https://"+origin] = struct{}{}
		policy.origins["http://"+origin] = struct{}{}
	}

	// Subdomain patterns: normalize *.example.com / example.com to a bare domain
	// and compile it into ^https?://(.*\.)?example\.com$; an invalid entry is an
	// error rather than being ignored.
	// 子域模式：先把 *.example.com / example.com 规范成纯域名，再编译成
	// ^https?://(.*\.)?example\.com$ 形式；非法配置直接报错。
	for _, raw := range cfg.AllowOriginPatterns {
		domain, ok := normalizeOriginPatternDomain(raw)
		if !ok {
			return nil, fmt.Errorf("support: cors invalid origin pattern %q", raw)
		}
		policy.patterns = append(policy.patterns, regexp.MustCompile(
			`(?i)^https?://(?:.*\.)?`+regexp.QuoteMeta(domain)+`$`))
	}

	// Path allowlist: an empty configuration equals ["*"]; matching is done
	// against the RequestURI including the query.
	// 路径白名单：空配置等价于 ["*"]；匹配对象是含 query 的 RequestURI。
	paths := cfg.AllowPaths
	if len(paths) == 0 {
		paths = []string{"*"}
	}
	for _, pattern := range paths {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			return nil, fmt.Errorf("support: cors path pattern must not be empty")
		}
		compiled, err := compileGlob(pattern)
		if err != nil {
			return nil, fmt.Errorf("support: cors invalid path pattern %q: %w", pattern, err)
		}
		policy.paths = append(policy.paths, compiled)
	}

	// Method and header defaults match PHP: the common method set, and "*" for
	// request headers.
	// 方法与请求头默认值与 PHP 一致：方法为常见全集，请求头为 "*"。
	methods := cfg.AllowMethods
	if len(methods) == 0 {
		methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	}
	headers := cfg.AllowHeaders
	if len(headers) == 0 {
		headers = []string{"*"}
	}
	policy.methods = strings.Join(methods, ", ")
	policy.headers = strings.Join(headers, ", ")
	policy.expose = strings.Join(cfg.ExposeHeaders, ", ")

	// MaxAge 0 counts as unconfigured and falls back to 24h (86400 seconds, the
	// PHP default).
	// MaxAge 0 视为未配置，使用 24h（86400 秒，与 PHP 默认一致）。
	maxAge := cfg.MaxAge
	if maxAge == 0 {
		maxAge = 24 * time.Hour
	}
	policy.maxAge = int(maxAge / time.Second)
	return policy, nil
}

// handle is the request-time entry point: validate Origin and path first, then
// write the headers; OPTIONS returns 204 whether or not it is a cross-origin
// request. A rejection is plain-text 403 with the same wording as PHP.
// handle 是请求期入口：先校验 Origin 与路径，再写响应头；OPTIONS 无论
// 是否跨域都返回 204。拒绝时返回纯文本 403，文案与 PHP 一致。
func (p *corsPolicy) handle(c *gin.Context) {
	origin := c.GetHeader("Origin")
	if origin != "" {
		// Origin outside the allowlist/patterns: reject here, never hand the
		// request to the business handler.
		// 来源不在白名单/模式内：直接拒绝，不再交给业务 handler。
		if !p.originAllowed(origin) {
			c.String(http.StatusForbidden, "`Origin` %d Forbidden", http.StatusForbidden)
			c.Abort()
			return
		}
		// The path allowlist matches the RequestURI including the query, like
		// PHP getRequestUri.
		// 路径白名单使用含 query 的 RequestURI，与 PHP getRequestUri 一致。
		uri := c.Request.URL.RequestURI()
		if !p.pathAllowed(uri) {
			c.String(http.StatusForbidden, "api %d Forbidden, path=%s", http.StatusForbidden, uri)
			c.Abort()
			return
		}
		p.writeHeaders(c, origin)
	}

	// As in PHP: OPTIONS always ends with 204 and never reaches routing.
	// 与 PHP 一致：OPTIONS 统一结束为 204，不再进入路由。
	if c.Request.Method == http.MethodOptions {
		c.AbortWithStatus(http.StatusNoContent)
		return
	}
	c.Next()
}

// originAllowed checks the wildcard, the exact origin set and then the subdomain
// patterns, in that order.
// originAllowed 依次检查任意来源、精确来源集合与子域模式。
func (p *corsPolicy) originAllowed(origin string) bool {
	if p.allowAll {
		return true
	}
	if _, ok := p.origins[origin]; ok {
		return true
	}
	for _, pattern := range p.patterns {
		if pattern.MatchString(origin) {
			return true
		}
	}
	return false
}

// pathAllowed matches the RequestURI against the precompiled fnmatch regexps.
// pathAllowed 用预编译的 fnmatch 正则匹配 RequestURI。
func (p *corsPolicy) pathAllowed(uri string) bool {
	for _, pattern := range p.paths {
		if pattern.MatchString(uri) {
			return true
		}
	}
	return false
}

// writeHeaders matches PHP setCorsHeaders: "*" is written when every origin is
// allowed and credentials are off, otherwise the concrete Origin is echoed;
// credential mode adds Allow-Credentials; every request carrying Origin gets
// Methods/Headers/Max-Age and Vary.
// writeHeaders 与 PHP setCorsHeaders 一致：允许所有来源且不带凭证时写 "*"，
// 否则回显具体 Origin；凭证模式追加 Allow-Credentials；所有带 Origin 的请求
// 都写 Methods/Headers/Max-Age 与 Vary。
func (p *corsPolicy) writeHeaders(c *gin.Context, origin string) {
	h := c.Writer.Header()
	if p.allowAll && !p.credentials {
		h.Set("Access-Control-Allow-Origin", "*")
	} else {
		h.Set("Access-Control-Allow-Origin", origin)
	}
	if p.credentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	h.Set("Vary", "Origin")
	h.Set("Access-Control-Allow-Methods", p.methods)
	h.Set("Access-Control-Allow-Headers", p.headers)
	if p.expose != "" {
		h.Set("Access-Control-Expose-Headers", p.expose)
	}
	if p.maxAge > 0 {
		h.Set("Access-Control-Max-Age", strconv.Itoa(p.maxAge))
	}
}

var (
	// originSchemePattern recognizes a full origin carrying a scheme.
	// originSchemePattern 识别带 scheme 的完整来源。
	originSchemePattern = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)
	// domainPattern is a lenient domain shape check (label.label..., localhost
	// allowed).
	// domainPattern 是宽松的域名形态校验（label.label...，允许 localhost）。
	domainPattern = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
)

// normalizeOriginPatternDomain normalizes *.example.com / example.com to a bare
// domain; schemes, paths, queries, ports and "@" are rejected so a
// misconfiguration cannot be read as a URL.
// normalizeOriginPatternDomain 把 *.example.com / example.com 规范为纯域名；
// 拒绝 scheme、path、query、端口、@ 等非法配置，避免配置被误当成 URL。
func normalizeOriginPatternDomain(pattern string) (string, bool) {
	pattern = strings.Trim(strings.TrimSpace(pattern), "/")
	if pattern == "" {
		return "", false
	}
	if originSchemePattern.MatchString(pattern) || strings.ContainsAny(pattern, "/?#@:") {
		return "", false
	}
	pattern = strings.ToLower(strings.TrimPrefix(pattern, "*."))
	if !domainPattern.MatchString(pattern) {
		return "", false
	}
	return pattern, true
}

// hasOriginScheme reports whether the origin starts with scheme://.
// hasOriginScheme 判断来源是否以 scheme:// 开头。
func hasOriginScheme(origin string) bool {
	return originSchemePattern.MatchString(origin)
}

// compileGlob compiles shell wildcards (*, ?, [seq]) into a regexp: like PHP
// fnmatch by default, * crosses /, so "/api/*" matches the whole RequestURI
// including the query. Character classes accept both [abc] and [!abc].
// compileGlob 把 shell 通配符（*、?、[seq]）编译成正则：
// 与 PHP fnmatch 默认行为一致，* 会跨越 /，因此 "/api/*" 能匹配含 query 的
// 完整 RequestURI。字符类支持 [abc] 与 [!abc]。
func compileGlob(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 0 {
				b.WriteString(regexp.QuoteMeta("["))
				continue
			}
			class := pattern[i+1 : i+1+end]
			i += end + 1
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
