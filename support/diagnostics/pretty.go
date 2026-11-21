package diagnostics

// The pretty formatter turns the Prometheus exposition served by /metrics into
// a table meant for humans: families grouped under one header, exporter noise
// labels dropped, histograms merged into count/sum with approximate
// quantiles. The scrape endpoint itself stays byte-for-byte standard — this is
// a view, not the wire format, and a Prometheus job must keep scraping
// MetricsPath, never the formatted route.
// pretty 格式化器把 /metrics 的 Prometheus exposition 转成给人看的表格：每个
// family 一段、隐去 exporter 内部标签、直方图合并成 count/sum 与近似分位数。
// 抓取端点保持逐字节的标准格式——这只是视图，不是线上格式；Prometheus 作业要
// 继续抓 MetricsPath，永远不要抓格式化路由。

import (
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// noiseLabels are exporter-internal scopes; they are identical on every series
// and only widen the tables.
// noiseLabels 是 exporter 内部的 scope 标签；每条序列都一样，只会撑宽表格。
var noiseLabels = map[string]struct{}{
	"otel_scope_name":       {},
	"otel_scope_schema_url": {},
	"otel_scope_version":    {},
}

var (
	helpLineRE = regexp.MustCompile(`#\s+HELP\s+(\S+)\s+(.*)`)
	typeLineRE = regexp.MustCompile(`#\s+TYPE\s+(\S+)\s+(\S+)`)
	// Label values may contain escaped quotes and commas; match one pair at a
	// time instead of splitting on commas.
	// 标签值可能包含转义的引号与逗号；逐对匹配，而不是按逗号切分。
	labelPairRE = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"`)
)

// quantile levels mirror the pretty printer contract: rough ordering is what an
// operator wants from a curl, exact percentiles belong in Prometheus.
// 分位数与 pretty 工具的契约一致：curl 时要的是大致排序，精确分位数交给 Prometheus。
var quantileLevels = []float64{0.5, 0.9, 0.99}

// identityOrder puts who/where labels first so rows scan like a sentence.
// identityOrder 把身份维度标签放前面，让每行读起来像一句话。
var identityOrder = []string{"group", "name", "consumer", "job", "broker", "topic", "outcome", "reason", "stage", "op"}

// sample is one exposition line with its labels already filtered.
// sample 是一行 exposition 样本，标签已完成过滤。
type sample struct {
	name   string
	labels map[string]string
	value  float64
}

// family is a group of samples sharing a TYPE-declared base name; suffix
// records which histogram part the sample was.
// family 是共享 TYPE 声明基名的一组样本；suffix 记录样本属于直方图的哪个部分。
type family struct {
	name    string
	typ     string
	help    string
	samples []sample
}

// captureWriter records what an inner handler wrote so the formatter can parse
// it before anything reaches the client.
// captureWriter 记录内层 handler 的输出，让格式化器在响应到达客户端之前解析它。
type captureWriter struct {
	header  http.Header
	body    strings.Builder
	status  int
	flushed bool
}

func (c *captureWriter) Header() http.Header { return c.header }

func (c *captureWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(p)
}

func (c *captureWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

// Flush satisfies http.Flusher; promhttp flushes periodically and would panic
// through a type assertion if the wrapper lacked it.
// Flush 实现 http.Flusher；promhttp 会周期性 flush，包装层缺这个方法会让类型
// 断言直接 panic。
func (c *captureWriter) Flush() { c.flushed = true }

// parseExposition splits a scrape body into families. Unknown types and
// summaries fall back to plain lists, which is the right loss for a viewer.
// parseExposition 把抓取正文拆成 family。未知类型与 summary 退化为普通列表，
// 对查看工具来说这是正确的损失。
func parseExposition(body string) []family {
	var (
		helps = map[string]string{}
		typs  = map[string]string{}
		all   []sample
	)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "#"):
			if m := helpLineRE.FindStringSubmatch(line); m != nil {
				helps[m[1]] = strings.TrimSpace(m[2])
			}
			if m := typeLineRE.FindStringSubmatch(line); m != nil {
				typs[m[1]] = m[2]
			}
		case strings.TrimSpace(line) == "":
		default:
			name, labels, value, ok := parseSampleLine(line)
			if ok {
				all = append(all, sample{name: name, labels: labels, value: value})
			}
		}
	}

	// Histogram samples carry a _bucket/_sum/_count suffix and belong to the
	// TYPE-declared base family; everything else groups by its own name.
	// 直方图样本带 _bucket/_sum/_count 后缀，归属 TYPE 声明的基名 family；
	// 其余样本按自己的名字分组。
	byName := map[string]*family{}
	var order []string
	place := func(fam string) *family {
		f, ok := byName[fam]
		if !ok {
			f = &family{name: fam, typ: typs[fam], help: helps[fam]}
			byName[fam] = f
			order = append(order, fam)
		}
		return f
	}
	for _, s := range all {
		base, suffix := s.name, ""
		for _, suf := range []string{"_bucket", "_sum", "_count"} {
			if strings.HasSuffix(s.name, suf) && typs[strings.TrimSuffix(s.name, suf)] == "histogram" {
				base, suffix = strings.TrimSuffix(s.name, suf), suf
				break
			}
		}
		f := place(base)
		// The sample name stores the histogram part (_bucket/_sum/_count) so
		// the renderer can dispatch; plain families keep their full name.
		// 样本名保存直方图部位（_bucket/_sum/_count）供渲染分发；普通 family
		// 保留全名。
		f.samples = append(f.samples, sample{name: suffix, labels: s.labels, value: s.value})
	}
	fams := make([]family, 0, len(order))
	for _, name := range order {
		fams = append(fams, *byName[name])
	}
	return fams
}

func parseSampleLine(line string) (string, map[string]string, float64, bool) {
	var name, labelPart, valuePart string
	if i := strings.IndexByte(line, '{'); i >= 0 {
		end := strings.LastIndexByte(line, '}')
		if end < i {
			return "", nil, 0, false
		}
		name, labelPart, valuePart = line[:i], line[i+1:end], line[end+1:]
	} else {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return "", nil, 0, false
		}
		name, valuePart = parts[0], parts[len(parts)-1]
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(valuePart), 64)
	if err != nil {
		return "", nil, 0, false
	}
	labels := map[string]string{}
	for _, m := range labelPairRE.FindAllStringSubmatch(labelPart, -1) {
		if _, noise := noiseLabels[m[1]]; noise {
			continue
		}
		labels[m[1]] = m[2]
	}
	return name, labels, value, true
}

// leFloat converts the le label; +Inf keeps its sentinel meaning and anything
// unparseable behaves like the open-ended bucket.
// leFloat 转换 le 标签；+Inf 保留哨兵含义，解析失败的值按无上界桶处理。
func leFloat(le string) float64 {
	if le == "+Inf" || le == "Inf" || le == "inf" {
		return math.Inf(1)
	}
	v, err := strconv.ParseFloat(le, 64)
	if err != nil {
		return math.Inf(1)
	}
	return v
}

// approxQuantile interpolates inside the bucket that contains the target rank.
// A rank past the last finite bucket can only be bounded from below.
// approxQuantile 在包含目标秩的桶内做线性插值。秩超出最后一个有限桶时只能给出下界。
func approxQuantile(q float64, buckets [][2]float64, count float64) (float64, bool, float64) {
	if count <= 0 {
		return 0, false, 0
	}
	target := q * count
	prevLE, prevCum := 0.0, 0.0
	for _, b := range buckets {
		le, cum := b[0], b[1]
		if math.IsInf(le, 1) {
			return prevLE, false, prevLE
		}
		if cum >= target {
			if cum == prevCum {
				return le, true, le
			}
			return prevLE + (le-prevLE)*(target-prevCum)/(cum-prevCum), true, le
		}
		prevLE, prevCum = le, cum
	}
	return 0, false, 0
}

func fmtSeconds(v float64) string {
	a := math.Abs(v)
	switch {
	case a >= 1:
		return fmt.Sprintf("%.2fs", v)
	case a >= 1e-3:
		return fmt.Sprintf("%.1fms", v*1e3)
	case a > 0:
		return fmt.Sprintf("%.0fµs", v*1e6)
	default:
		return "0"
	}
}

func fmtNumber(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	if math.Abs(v) < 1000 {
		return strconv.FormatFloat(v, 'g', 4, 64)
	}
	return strconv.FormatFloat(v, 'f', 0, 64)
}

// orderLabels renders k=v pairs with identity labels first, rest in sorted
// order for stable output.
// orderLabels 渲染 k=v 对：身份标签在前，其余按排序输出保证稳定。
func orderLabels(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for _, k := range identityOrder {
		if v, ok := labels[k]; ok {
			parts = append(parts, k+"="+v)
		}
	}
	rest := make([]string, 0, len(labels))
	for k := range labels {
		if _, known := identityIndex()[k]; !known {
			rest = append(rest, k+"="+labels[k])
		}
	}
	sort.Strings(rest)
	parts = append(parts, rest...)
	return strings.Join(parts, " ")
}

var identityIndexCache map[string]struct{}

func identityIndex() map[string]struct{} {
	if identityIndexCache == nil {
		identityIndexCache = map[string]struct{}{}
		for _, k := range identityOrder {
			identityIndexCache[k] = struct{}{}
		}
	}
	return identityIndexCache
}

// renderFamilies produces the human-readable text for a set of families.
// renderFamilies 为一组 family 生成人类可读文本。
func renderFamilies(fams []family) string {
	var out strings.Builder
	for _, f := range fams {
		out.WriteString("\n■ ")
		out.WriteString(f.name)
		out.WriteString("  [")
		if f.typ == "" {
			out.WriteString("untyped")
		} else {
			out.WriteString(f.typ)
		}
		out.WriteString("]")
		if f.help != "" {
			out.WriteString(" — ")
			out.WriteString(f.help)
		}
		out.WriteString("\n")

		if f.typ == "histogram" {
			renderHistogram(&out, f)
			continue
		}

		type row struct{ left, right string }
		rows := make([]row, 0, len(f.samples))
		width := len("label")
		for _, s := range f.samples {
			left := orderLabels(s.labels)
			if len(left) > width {
				width = len(left)
			}
			rows = append(rows, row{left, fmtNumber(s.value)})
		}
		valueHeader := len("value")
		if len(rows) == 0 {
			out.WriteString("  (no series)\n")
			continue
		}
		out.WriteString("  " + pad("label", width) + "  " + pad("value", valueHeader) + "\n")
		for _, r := range rows {
			out.WriteString("  " + pad(r.left, width) + "  " + strings.Repeat(" ", maxInt(0, valueHeader-len(r.right))) + r.right + "\n")
		}
	}
	return out.String()
}

func renderHistogram(out *strings.Builder, f family) {
	// Merge bucket/sum/count samples sharing a label set without le.
	// 合并共享同一标签集（不含 le）的 bucket/sum/count 样本。
	type agg struct {
		count   float64
		sum     float64
		buckets [][2]float64
	}
	merged := map[string]*agg{}
	var keys []string
	for _, s := range f.samples {
		withoutLE := make(map[string]string, len(s.labels))
		for k, v := range s.labels {
			if k != "le" {
				withoutLE[k] = v
			}
		}
		key := orderLabels(withoutLE)
		a, ok := merged[key]
		if !ok {
			a = &agg{}
			merged[key] = a
			keys = append(keys, key)
		}
		switch s.name {
		case "_count":
			a.count = s.value
		case "_sum":
			a.sum = s.value
		case "_bucket":
			a.buckets = append(a.buckets, [2]float64{leFloat(s.labels["le"]), s.value})
		}
	}
	sort.Strings(keys)

	width := len("label")
	for _, key := range keys {
		if len(key) > width {
			width = len(key)
		}
	}
	out.WriteString("  " + pad("label", width) + "  count / sum / quantiles\n")
	for _, key := range keys {
		a := merged[key]
		sort.Slice(a.buckets, func(i, j int) bool { return a.buckets[i][0] < a.buckets[j][0] })
		var b strings.Builder
		b.WriteString("count=" + fmtNumber(a.count))
		b.WriteString(" sum=" + fmtSeconds(a.sum))
		for _, q := range quantileLevels {
			v, ok, bound := approxQuantile(q, a.buckets, a.count)
			b.WriteString("  p" + strconv.Itoa(int(q*100)) + "≈")
			switch {
			case ok:
				b.WriteString(fmtSeconds(v))
			case bound > 0:
				// The rank fell into the +Inf bucket; only a lower bound exists.
				// 秩落进 +Inf 桶，只能给出下界。
				b.WriteString(">" + fmtSeconds(bound))
			default:
				b.WriteString("-")
			}
		}
		out.WriteString("  " + pad(key, width) + "  " + b.String() + "\n")
	}
}

func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// prettyMetrics wraps the scrape handler and serves the formatted view. A
// non-200 from the inner handler is passed through untouched — an error page
// must not be reinterpreted as exposition.
// prettyMetrics 包装抓取 handler 并提供格式化视图。内层 handler 非 200 的响应
// 原样透传——错误页不能被当成 exposition 重新解释。
func prettyMetrics(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ask the inner handler for plain text: dropping Accept-Encoding keeps
		// promhttp from gzipping the body we are about to parse.
		// 向内层 handler 索要纯文本：去掉 Accept-Encoding，避免 promhttp 把即将
		// 解析的正文 gzip 起来。
		innerReq := r.Clone(r.Context())
		innerReq.Header.Del("Accept-Encoding")
		cap := &captureWriter{header: http.Header{}}
		inner.ServeHTTP(cap, innerReq)

		if cap.status != http.StatusOK {
			copyHeader(w.Header(), cap.header)
			w.WriteHeader(cap.status)
			_, _ = w.Write([]byte(cap.body.String()))
			return
		}

		text := renderFamilies(parseExposition(cap.body.String()))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(text))
	})
}

func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
