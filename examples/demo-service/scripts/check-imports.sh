#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

fail() {
  echo "demo import guard failed: $*" >&2
  exit 1
}

module="github.com/yourorg/demo-service"
module_prefix="$module/app/module/"

# 只检查非测试文件的 import（go list 的 .Imports 不含 _test.go），测试可以
# 同时引用多个模块的 fake。包依赖成环由 go build 直接报错，这里不重复检查。
go list -f '{{.ImportPath}}|{{join .Imports " "}}' ./app/... |
while IFS='|' read -r pkg imports; do
  case "$pkg" in
    "$module_prefix"*)
      rest=${pkg#"$module_prefix"}
      owner=${rest%%/*}
      case "$rest" in
        */controller|*/controller/*)
          # controller 只依赖本模块 service，使用契约类型；不碰持久化。
          for imp in $imports; do
            case "$imp" in
              "$module/app/repository"|"$module/app/repository/"*)
                fail "$pkg must not import $imp: controllers depend on their own service only"
                ;;
              "$module_prefix"*)
                dep=${imp#"$module_prefix"}
                dep_owner=${dep%%/*}
                case "$dep" in
                  */model|*/model/*)
                    fail "$pkg must not import $imp: controllers use contract types, not persistence models"
                    ;;
                  */repository|*/repository/*)
                    fail "$pkg must not import $imp: controllers must not reach the persistence layer"
                    ;;
                  */service|*/service/*)
                    if [ "$dep_owner" != "$owner" ]; then
                      fail "$pkg must not import $imp: controllers depend on their own service only"
                    fi
                    ;;
                esac
                ;;
            esac
          done
          ;;
        */repository)
          # repository 只做本聚合的数据访问，不能反向依赖 service。
          for imp in $imports; do
            case "$imp" in
              "$module_prefix"*/service|"$module_prefix"*/service/*)
                fail "$pkg must not import $imp: repositories never depend on services"
                ;;
            esac
          done
          ;;
      esac
      ;;
  esac

  # 只有 app/repository 可以聚合多个模块的 repository；其他包最多 import
  # 一个“其他模块”的 repository，本模块的不计入。
  case "$pkg" in
    "$module/app/repository") continue ;;
  esac
  own=""
  case "$pkg" in
    "$module_prefix"*)
      rest=${pkg#"$module_prefix"}
      own=${rest%%/*}
      ;;
  esac
  seen=""
  count=0
  for imp in $imports; do
    case "$imp" in
      "$module_prefix"*/repository)
        dep=${imp#"$module_prefix"}
        dep_owner=${dep%%/*}
        if [ "$dep_owner" = "$own" ]; then
          continue
        fi
        case " $seen " in
          *" $dep_owner "*) ;;
          *)
            seen="$seen $dep_owner"
            count=$((count + 1))
            ;;
        esac
        ;;
    esac
  done
  if [ "$count" -gt 1 ]; then
    fail "$pkg imports repositories from multiple other modules:$seen; only $module/app/repository may aggregate them"
  fi
done

echo "demo import guards passed"
