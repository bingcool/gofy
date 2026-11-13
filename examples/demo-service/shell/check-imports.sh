#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

fail() {
  echo "demo import guard failed: $*" >&2
  exit 1
}

module="github.com/yourorg/demo-service"
module_prefix="$module/app/module/"
appio_prefix="$module/app/io"
contract_prefix="github.com/bingcool/gofy-interface-service"
support_prefix="github.com/bingcool/gofy/support"
framework_prefix="github.com/bingcool/gofy"

# 标准库集合：innerapi 是纯契约，只允许标准库（必要时 support 的纯类型），
# 用它把标准库与第三方依赖区分开。
stdlib_list="$(go list std)"
is_stdlib() {
  printf '%s\n' "$stdlib_list" | grep -qx "$1"
}

# 只检查非测试文件的 import（go list 的 .Imports 不含 _test.go）：测试可以同时
# 引用多个模块的 model/repository 建 SQLite 库。按层校验，规则见
# docs/module-layering-design.md §10。包依赖成环由 go build 兜底（innerapi 无回边，
# 天然无环），这里不重复检查。
go list -f '{{.ImportPath}}|{{join .Imports " "}}' ./app/... |
while IFS='|' read -r pkg imports; do
  case "$pkg" in
    "$module_prefix"*) ;;
    *) continue ;;
  esac

  rest=${pkg#"$module_prefix"}
  owner=${rest%%/*}
  layer=${rest#"$owner"}
  layer=${layer#/}
  layer=${layer%%/*}

  # 模块根包（module.go）是装配/接线层，合法 import 本模块各层、其他模块
  # innerapi 与契约路由，不受分层规则约束。
  [ -n "$layer" ] || continue

  for imp in $imports; do
    cat=""
    dep_owner=""
    dep_layer=""
    case "$imp" in
      "$module_prefix"*)
        cat="module"
        d=${imp#"$module_prefix"}
        dep_owner=${d%%/*}
        dl=${d#"$dep_owner"}
        dl=${dl#/}
        dep_layer=${dl%%/*}
        ;;
      "$contract_prefix"*) cat="contract" ;;
      "$appio_prefix"*) cat="appio" ;;
      "$support_prefix"*) cat="support" ;;
      "$framework_prefix"*) cat="framework" ;;
      *)
        if is_stdlib "$imp"; then cat="stdlib"; else cat="other"; fi
        ;;
    esac

    case "$layer" in
      controller)
        # 只依赖本模块 service 与 HTTP 契约；不碰 model/repository/innerapi/
        # 其他模块，也不直接取 app/io 句柄。
        case "$cat" in
          module)
            if [ "$dep_owner" != "$owner" ] || [ "$dep_layer" != "service" ]; then
              fail "$pkg must not import $imp: controllers depend on their own service only"
            fi
            ;;
          appio)
            fail "$pkg must not import $imp: controllers must not reach app/io handles"
            ;;
        esac
        ;;
      service)
        # 不依赖 HTTP 契约；跨模块只走对方 innerapi，不碰其他模块
        # service/repository/model；本模块各层与 app/io（开事务）允许。
        case "$cat" in
          contract)
            fail "$pkg must not import $imp: services must not depend on HTTP contracts"
            ;;
          module)
            if [ "$dep_owner" != "$owner" ] && [ "$dep_layer" != "innerapi" ]; then
              fail "$pkg must not import $imp: cross-module calls go through innerapi only"
            fi
            ;;
        esac
        ;;
      innerapi)
        # 纯契约：只允许标准库与 support 纯类型，禁止任何 app/module、契约、
        # app/io、框架与第三方依赖。
        case "$cat" in
          stdlib | support) ;;
          *)
            fail "$pkg must not import $imp: innerapi is a pure contract (stdlib/support only)"
            ;;
        esac
        ;;
      repository)
        # 只依赖本模块 model + app/io + support；不碰 service/innerapi/契约/
        # 其他模块。
        case "$cat" in
          module)
            if [ "$dep_owner" != "$owner" ] || [ "$dep_layer" != "model" ]; then
              fail "$pkg must not import $imp: repositories depend on their own model only"
            fi
            ;;
          contract)
            fail "$pkg must not import $imp: repositories must not depend on HTTP contracts"
            ;;
        esac
        ;;
      model)
        # 表结构不依赖任何 app/module、契约或 app/io。
        case "$cat" in
          module | contract | appio)
            fail "$pkg must not import $imp: models must not depend on app/module, contracts or app/io"
            ;;
        esac
        ;;
    esac
  done
done

echo "demo import guards passed"