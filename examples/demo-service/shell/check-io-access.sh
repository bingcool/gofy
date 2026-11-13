#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

fail() {
  echo "demo io-access guard failed: $*" >&2
  exit 1
}

# service/repository 是三种入口（HTTP、脚本、守护进程）共用的同一份代码，句柄
# 只能经 appio.WithContext(ctx) 取本代快照。直接读实例包的包级访问器，或自己
# 发布快照，都会绕开入口固定的那一代，给共用代码开出第二种取法。只检查非测试
# 文件：测试需要用 Publish / PublishDB 造快照。
pattern='appdb\.(Default|NewIO)\(|appredis\.(Cache|Session|RateLimit|NewIO)\(|appamqp\.(Default|Get|NewIO)\(|appkafka\.(Default|Get|NewIO)\(|approcketmq\.(Default|Get|NewIO)\(|appuuidint\.Default\(|appio\.(Current|Publish|PublishDB)\('

dirs="$(go list -f '{{.ImportPath}} {{.Dir}}' ./app/module/... | awk '$1 ~ /\/(service|repository)$/ {print $2}')"
[ -n "$dirs" ] || fail "no service or repository packages found"

failed=0
while IFS= read -r dir; do
  [ -n "$dir" ] || continue
  hits="$(grep -REn --include='*.go' --exclude='*_test.go' -e "$pattern" "$dir" || true)"
  if [ -n "$hits" ]; then
    printf '%s\n' "$hits" >&2
    failed=1
  fi
done <<EOF
$dirs
EOF

[ "$failed" -eq 0 ] || fail "service/repository must resolve handles through appio.WithContext(ctx), not package-level accessors or their own Publish"

echo "demo io-access guard passed"
