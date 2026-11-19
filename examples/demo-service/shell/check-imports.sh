#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

fail() {
  echo "demo import guard failed: $*" >&2
  exit 1
}

module="github.com/yourorg/demo-service"
module_prefix="$module/app/module/"
appconfig_prefix="$module/app/config"
appio_prefix="$module/app/appio"
contract_prefix="github.com/yourorg/interface-service"
api_contracts_prefix="github.com/bingcool/gofy-interface-api/contracts"
api_client_prefix="github.com/bingcool/gofy-interface-api/client"
adapter_gofy_prefix="github.com/bingcool/gofy-adapter/adapter/gofy"
adapter_prefix="github.com/bingcool/gofy-adapter/adapter"
adapter_client_prefix="github.com/bingcool/gofy-adapter/client"
apperr_prefix="github.com/bingcool/gofy/apperr"
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
    # 每个 import 先归类，再由各层的显式白名单决定取舍。各层没有 default 放行：
    # 未列出的类别（client、adapter 的其它包、HTTP 契约、其他模块…）一律失败。
    # 归类顺序有讲究：更具体的 adapter/gofy 与 apperr 必须排在其大类之前。
    token=""
    dep_owner=""
    dep_layer=""
    case "$imp" in
      "$module_prefix"*)
        token="module"
        d=${imp#"$module_prefix"}
        dep_owner=${d%%/*}
        dl=${d#"$dep_owner"}
        dl=${dl#/}
        dep_layer=${dl%%/*}
        ;;
      "$appconfig_prefix" | "$appconfig_prefix/"*) token="appconfig" ;;
      "$appio_prefix"*) token="appio" ;;
      "$contract_prefix"*) token="contract" ;;
      "$api_contracts_prefix"*) token="apicontract" ;;
      "$api_client_prefix"* | "$adapter_client_prefix"*) token="client" ;;
      "$adapter_gofy_prefix"*) token="adapter_gofy" ;;
      "$adapter_prefix"*) token="adapter" ;;
      "$apperr_prefix") token="apperr" ;;
      "$support_prefix"*) token="support" ;;
      "$framework_prefix"*) token="framework" ;;
      "go.uber.org/zap" | "go.uber.org/zap/"*) token="logging" ;;
      "gorm.io/gorm" | "gorm.io/gorm/"*) token="gorm" ;;
      *)
        if is_stdlib "$imp"; then token="stdlib"; else token="other"; fi
        ;;
    esac

    case "$layer" in
      controller)
        # 入口层：HTTP controller、script、worker 是三种平权入口，各自封装入口细节
        # （绑定与响应、命令行与信号、订阅与 drain），底层业务代码复用。因此 controller
        # 不再被限制"只能依赖自己的 service"：appio、config、query、repository、model、
        # 裸 SQL、框架与第三方库都可以直接用。
        #
        # 唯一保留的边界是模块边界：跨模块只走对方 innerapi 或 HTTP 契约。否则模块之间
        # 就没有边界，改一个模块会牵动所有入口。
        case "$token" in
          stdlib | appio | appconfig | support | framework | gorm | logging | other | \
          contract | apicontract | adapter_gofy | adapter | apperr | client) ;;
          module)
            if [ "$dep_owner" != "$owner" ] && [ "$dep_layer" != "innerapi" ]; then
              fail "$pkg must not import $imp: cross-module calls go through innerapi or the HTTP contract"
            fi
            ;;
        esac
        ;;
      service)
        # 白名单：本模块 model/repository/innerapi、其他模块 innerapi、app/appio、
        # support、本模块配置段与日志。HTTP 契约、adapter、client、controller 与
        # query 都不在名单里。
        case "$token" in
          stdlib | appio | support | appconfig | logging) ;;
          module)
            if [ "$dep_owner" = "$owner" ]; then
              case "$dep_layer" in
                model | repository | innerapi) ;;
                *)
                  fail "$pkg must not import $imp: services may import their own model, repository and innerapi only"
                  ;;
              esac
            else
              [ "$dep_layer" = "innerapi" ] || \
                fail "$pkg must not import $imp: cross-module calls go through innerapi only"
            fi
            ;;
          *)
            fail "$pkg must not import $imp: services may import own model/repository/innerapi, other modules' innerapi, app/appio, support, their config section and logging"
            ;;
        esac
        ;;
      innerapi)
        # 纯契约：只允许标准库与 support 纯类型，禁止任何 app/module、契约、
        # app/appio、框架与第三方依赖。
        case "$token" in
          stdlib | support) ;;
          *)
            fail "$pkg must not import $imp: innerapi is a pure contract (stdlib/support only)"
            ;;
        esac
        ;;
      repository)
        # 白名单：本模块 model、app/appio、support 与 ORM。不碰 service/
        # innerapi/契约/其他模块，也不在这里打日志。
        case "$token" in
          stdlib | appio | support | gorm) ;;
          module)
            if [ "$dep_owner" != "$owner" ] || [ "$dep_layer" != "model" ]; then
              fail "$pkg must not import $imp: repositories depend on their own model only"
            fi
            ;;
          *)
            fail "$pkg must not import $imp: repositories may import own model, app/appio, support and gorm only"
            ;;
        esac
        ;;
      model)
        # 表结构只用标准库：不依赖 app/module、契约、app/appio、框架或第三方。
        case "$token" in
          stdlib) ;;
          *)
            fail "$pkg must not import $imp: models must not depend on anything but the standard library"
            ;;
        esac
        ;;
      query)
        # 只读查询层：本模块 model、app/appio、support、框架与第三方库；不碰
        # service/controller/innerapi/repository、其他模块、契约、client 与 adapter。
        case "$token" in
          stdlib | appio | support | framework | gorm | logging | other) ;;
          module)
            if [ "$dep_owner" != "$owner" ] || [ "$dep_layer" != "model" ]; then
              fail "$pkg must not import $imp: query depends on its own model only"
            fi
            ;;
          *)
            fail "$pkg must not import $imp: query may import own model, app/appio, support, framework and third-party libraries only"
            ;;
        esac
        ;;
    esac
  done
done

echo "demo import guards passed"