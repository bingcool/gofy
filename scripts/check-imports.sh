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
for pkg in apperr core logging support/... script command config; do
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

# logging is independently usable.
if go list -deps ./logging | grep "$root_pkg/" | grep -qvx "$root_pkg/logging"; then
  fail "logging must not depend on other gofy packages"
fi

# support/db receives a *zap.Logger instead of importing logging.
if go list -deps ./support/db | grep -q "$root_pkg/logging"; then
  fail "support/db must not depend on logging"
fi

# support/pg receives a *zap.Logger instead of importing logging.
if go list -deps ./support/pg | grep -q "$root_pkg/logging"; then
  fail "support/pg must not depend on logging"
fi

# support/sqlite receives a *zap.Logger instead of importing logging.
if go list -deps ./support/sqlite | grep -q "$root_pkg/logging"; then
  fail "support/sqlite must not depend on logging"
fi

# support/uuidint must stay independent of the root package and logging.
if go list -deps ./support/uuidint | grep -q "$root_pkg/logging"; then
  fail "support/uuidint must not depend on logging"
fi

# support/phpoffice must stay independent of the root package and logging.
if go list -deps ./support/phpoffice | grep -q "$root_pkg/logging"; then
  fail "support/phpoffice must not depend on logging"
fi

echo "import guards passed"
