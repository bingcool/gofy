#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

fail() {
  echo "demo io-access guard failed: $*" >&2
  exit 1
}

# service/repository/query 是三种入口（HTTP、脚本、守护进程）共用的同一份代码，句柄
# 只能经 appio.WithContext(ctx) 取本代快照。直接读实例包的包级访问器，或自己
# 发布快照，都会绕开入口固定的那一代，给共用代码开出第二种取法。只检查非测试
# 文件：测试需要用 Publish / PublishDB 造快照。
pattern='appdb\.(Default|NewIO)\(|appredis\.(Cache|Session|RateLimit|NewIO)\(|appamqp\.(Default|Get|NewIO)\(|appkafka\.(Default|Get|NewIO)\(|approcketmq\.(Default|Get|NewIO)\(|appuuidint\.Default\(|appio\.(Current|Publish|PublishDB)\('

dirs="$(go list -f '{{.ImportPath}} {{.Dir}}' ./app/module/... | awk '$1 ~ /\/(service|repository|query)$/ {print $2}')"
[ -n "$dirs" ] || fail "no service, repository or query packages found"

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

[ "$failed" -eq 0 ] || fail "service/repository/query must resolve handles through appio.WithContext(ctx), not package-level accessors or their own Publish"

# worker 取连接同样只有一条路：进程启动时装配一代 IO（WithWorkerModules →
# appio.NewDBOnlyModuleFactory），worker 入口与请求一样经 appio.WithContext(ctx).DBIO
# 取句柄。Runtime.DB 是"框架托管池的引用"，只在装配层可见；worker 里读到它，说明
# 又在用 Runtime 当连接容器。发布代际同理只归装配点：worker 目录里出现 Publish，
# 就会有两个代际来源。
#
# 这两条都不设白名单。workerio 与 tenantguard 曾经需要豁免，是因为它们替缺失的装配
# 入口做了补丁；装配入口存在之后，豁免本身就是那个补丁的残留。
worker_db_hits="$(grep -REn --include='*.go' --exclude='*_test.go' -e '\brt\.DB\b' worker || true)"
if [ -n "$worker_db_hits" ]; then
  printf '%s\n' "$worker_db_hits" >&2
  failed=1
fi

worker_publish_hits="$(grep -REn --include='*.go' --exclude='*_test.go' -e 'appio\.(Publish|PublishDB|Current)\(' worker || true)"
if [ -n "$worker_publish_hits" ]; then
  printf '%s\n' "$worker_publish_hits" >&2
  failed=1
fi

[ "$failed" -eq 0 ] || fail "workers must take handles from appio.WithContext(ctx).DBIO; Runtime.DB and Publish belong to the assembly point"

echo "demo io-access guard passed"
