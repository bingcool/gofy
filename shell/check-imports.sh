#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

fail() {
  echo "import guard failed: $*" >&2
  exit 1
}

root_pkg="github.com/bingcool/gofy"

# Public packages must not depend on the root package: that would be an import
# cycle the moment the root package exposes them. gofytest is the deliberate
# exception: it wraps the root package for tests.
for pkg in apperr core logger support/... worker/... script command config; do
  if go list -deps "./$pkg" | grep -qx "$root_pkg"; then
    fail "$pkg must not depend on the root package"
  fi
done

# The root package must not depend on test helpers or examples.
if go list -deps . | grep -q "$root_pkg/gofytest"; then
  fail "root package must not depend on gofytest"
fi
if go list -deps . | grep -q "$root_pkg/cmd"; then
  fail "root package must not depend on cmd/gofy"
fi

# core is a leaf shared by root, script and command.
if go list -deps ./core | grep "$root_pkg/" | grep -qvx "$root_pkg/core"; then
  fail "core must not depend on other gofy packages"
fi

# logger is independently usable.
if go list -deps ./logger | grep "$root_pkg/" | grep -qvx "$root_pkg/logger"; then
  fail "logger must not depend on other gofy packages"
fi

# support/db receives a *zap.Logger instead of importing logger.
if go list -deps ./support/db | grep -q "$root_pkg/logger"; then
  fail "support/db must not depend on logger"
fi

# support/pg receives a *zap.Logger instead of importing logger.
if go list -deps ./support/pg | grep -q "$root_pkg/logger"; then
  fail "support/pg must not depend on logger"
fi

# support/sqlite receives a *zap.Logger instead of importing logger.
if go list -deps ./support/sqlite | grep -q "$root_pkg/logger"; then
  fail "support/sqlite must not depend on logger"
fi

# support/uuidint must stay independent of the root package and logger.
if go list -deps ./support/uuidint | grep -q "$root_pkg/logger"; then
  fail "support/uuidint must not depend on logger"
fi

# support/phpoffice must stay independent of the root package and logger.
if go list -deps ./support/phpoffice | grep -q "$root_pkg/logger"; then
  fail "support/phpoffice must not depend on logger"
fi

# support/image must stay independent of the root package and logger.
if go list -deps ./support/image | grep -q "$root_pkg/logger"; then
  fail "support/image must not depend on logger"
fi

# support/sqlbuilder 只构造 SQL 字符串，不执行、不持有连接。引入 gorm 就意味着有人把
# 执行栈塞了进来——那时连接、事务、日志都要重新接一遍，它也就不是构造器了。
if go list -deps ./support/sqlbuilder | grep -q "gorm.io/gorm"; then
  fail "support/sqlbuilder must not depend on gorm"
fi

# gofy 框架库不得反向依赖契约基础设施库。
if go list -deps ./... | grep -q 'github.com/bingcool/gofy-interface-api'; then
  fail "gofy must not depend on gofy-interface-api"
fi
if go list -deps ./... | grep -q 'github.com/bingcool/gofy-adapter'; then
  fail "gofy must not depend on gofy-adapter"
fi

echo "import guards passed"
