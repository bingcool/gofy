#!/usr/bin/env python3
"""metrics-pretty.py — 把 /metrics 的 Prometheus exposition 输出转成人类可读格式。

用法（任选其一）:
  curl -s http://127.0.0.1:6061/metrics | python3 shell/metrics-pretty.py
  python3 shell/metrics-pretty.py http://127.0.0.1:6061/metrics
  python3 shell/metrics-pretty.py saved-scrape.txt     # 保存过的抓取文件
  curl -s ... | python3 shell/metrics-pretty.py -      # 显式 stdin

说明:
- 直方图合并 _bucket/_sum/_count，给出 count/sum 与近似 p50/p90/p99（桶内线性插值）。
- otel_scope_* 标签是 exporter 内部信息，一律隐去。
- 只用标准库，Prometheus 抓取端点保持标准格式不受影响。
"""
import re
import sys
import urllib.request

LABEL_RE = re.compile(r'([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"')
HELP_RE = re.compile(r"#\s+HELP\s+(\S+)\s+(.*)")
TYPE_RE = re.compile(r"#\s+TYPE\s+(\S+)\s+(\S+)")
NOISE = {"otel_scope_name", "otel_scope_schema_url", "otel_scope_version"}
QUANTILES = (0.5, 0.9, 0.99)
# 标签显示顺序：身份维度在前，结果维度在后，其余按出现顺序。
IDENTITY = ["group", "name", "consumer", "job", "broker", "topic", "outcome", "reason", "stage", "op"]


def fetch(arg):
    if arg in ("-", None):
        return sys.stdin.read()
    if arg.startswith(("http://", "https://")):
        with urllib.request.urlopen(arg, timeout=5) as resp:
            return resp.read().decode()
    with open(arg, encoding="utf-8") as fh:
        return fh.read()


def parse_labels(text):
    return {k: v for k, v in LABEL_RE.findall(text) if k not in NOISE}


def label_key(labels):
    return tuple(sorted(labels.items()))


def sort_labels(labels):
    rest = [(k, v) for k, v in labels.items() if k not in IDENTITY]
    ordered = [(k, labels[k]) for k in IDENTITY if k in labels]
    return ordered + rest


def fmt_num(x):
    if x == int(x) and abs(x) < 1e15:
        return f"{int(x):,}"
    if abs(x) < 1000:
        return f"{x:.4g}"
    return f"{x:,.0f}"


def fmt_seconds(s):
    a = abs(s)
    if a >= 1:
        return f"{s:.2f}s"
    if a >= 1e-3:
        return f"{s * 1000:.1f}ms"
    if a > 0:
        return f"{s * 1e6:.0f}µs"
    return "0"


def le_float(le):
    """`le` 标签是字符串；+Inf 返回 math.inf，其余转 float。"""
    if le in ("+Inf", "Inf", "inf"):
        return float("inf")
    try:
        return float(le)
    except ValueError:
        return float("inf")


def approx_quantile(q, buckets, count):
    """桶内线性插值求近似分位数；落入 +Inf 桶时返回 '>上界' 形式的下界。"""
    if count <= 0:
        return None
    target = q * count
    prev_le, prev_cum = 0.0, 0.0
    for le, cum in buckets:
        if le == float("inf"):
            return f">{fmt_seconds(prev_le).lstrip('>')}" if prev_le else None
        if cum >= target:
            if cum == prev_cum:
                return le
            return prev_le + (le - prev_le) * (target - prev_cum) / (cum - prev_cum)
        prev_le, prev_cum = le, cum
    return None


def parse(text):
    help_text, type_text, samples = {}, {}, []
    for line in text.splitlines():
        if line.startswith("#"):
            if m := HELP_RE.match(line):
                help_text[m.group(1)] = m.group(2).strip()
            if m := TYPE_RE.match(line):
                type_text[m.group(1)] = m.group(2)
            continue
        if not line.strip():
            continue
        if "{" in line:
            name, rest = line.split("{", 1)
            labels_str, _, value = rest.rpartition("}")
            samples.append((name, parse_labels(labels_str), float(value.strip().split()[0])))
        else:
            parts = line.split()
            samples.append((parts[0], {}, float(parts[-1])))
    return help_text, type_text, samples


def render_rows(header, rows):
    """rows: [(label_text, value_text)]；对齐输出。"""
    width = max([len(t) for t, _ in rows] + [len(header)])
    print(f"  {'label'.ljust(width)}  {header}")
    for text, value in rows:
        print(f"  {text.ljust(width)}  {value.rjust(len(header))}")


def main():
    text = fetch(sys.argv[1] if len(sys.argv) > 1 else None)
    help_text, type_text, samples = parse(text)

    # 直方图的 _bucket/_sum/_count 归并回同一 family；其余按样本名分组。
    families, order = {}, []
    for name, labels, value in samples:
        fam, suffix = name, None
        for s in ("_bucket", "_sum", "_count"):
            if name.endswith(s) and type_text.get(name[: -len(s)]) == "histogram":
                fam, suffix = name[: -len(s)], s[1:]
                break
        if fam not in families:
            families[fam] = []
            order.append(fam)
        families[fam].append((labels, value, suffix))

    for fam in order:
        typ = type_text.get(fam, "untyped")
        desc = f" — {help_text[fam]}" if fam in help_text else ""
        print(f"\n■ {fam}  [{typ}]{desc}")
        rows = families[fam]

        if typ == "histogram":
            merged = {}
            for labels, value, suffix in rows:
                key = label_key({k: v for k, v in labels.items() if k != "le"})
                cnt, sm, buckets = merged.get(key, (0, 0.0, []))
                if suffix == "count":
                    cnt = value
                elif suffix == "sum":
                    sm = value
                elif suffix == "bucket":
                    buckets.append((le_float(labels.get("le", "")), value))
                merged[key] = (cnt, sm, buckets)

            prepared = []
            for key, (cnt, sm, buckets) in merged.items():
                buckets.sort(key=lambda b: b[0])
                qs = [approx_quantile(q, buckets, cnt) for q in QUANTILES]
                prepared.append((dict(key), cnt, sm, qs))
            prepared.sort(key=lambda r: label_key(r[0]))

            rendered = []
            for labels, cnt, sm, qs in prepared:
                left = " ".join(f"{k}={v}" for k, v in sort_labels(labels))
                right = f"count={fmt_num(cnt)} sum={fmt_seconds(sm)}"
                for q, val in zip(QUANTILES, qs):
                    if val is None:
                        shown = "-"
                    elif isinstance(val, str):
                        shown = val
                    else:
                        shown = fmt_seconds(val)
                    right += f"  p{int(q * 100)}≈{shown}"
                rendered.append((left, right))
            render_rows("count / sum / quantiles", rendered)
        else:
            rendered = [
                (" ".join(f"{k}={v}" for k, v in sort_labels(labels)), fmt_num(value))
                for labels, value, _ in rows
            ]
            render_rows("value", rendered)

    print()


if __name__ == "__main__":
    main()
