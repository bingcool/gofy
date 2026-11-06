# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `support/headerpropagation`: captures a whitelist of request headers
  (`X-Trace-Id`, `X-User-*`, `X-Tenant-Id`, `X-Client-Ip`, `X-User-Agent`) and
  carries them in the request context. Identity headers are trusted only when
  the direct peer matches the configured proxies; plain clients cannot spoof a
  tenant, and credentials such as `Authorization` never propagate.
- `WithHeaderPropagation` and `transport/middleware.HeaderPropagation`: install
  that capture in front of the business middleware. The trace id falls back to
  the generated request id. The context key is shared with
  `gofy-interface-service/client`, which forwards the headers on downstream
  calls without either module importing the other.
- `support/frameworkcontext`: one identity entry point for handlers, service
  layers and repositories. It snapshots a locally verified user and falls back
  to trusted propagated headers, so `UserID(ctx)` works both in the gateway
  and in downstream services. `WithUser` clears stale identity headers and
  writes the local user and tenant back for downstream calls, and
  `UserFromClaims` maps `user_id`/`uid`/`sub`, `tenant_id`, `user_code` and
  `user_name`. `JWTAuth` populates it automatically after verification.
- `support/headerpropagation`: `ContextWithoutHeader` removes one whitelisted
  header, used when a local identity replaces one captured from upstream.
- `support/jwtauth`: HMAC JWT verification for HS256/HS384/HS512. It validates
  signature, `exp`/`nbf` with optional leeway, `iss` and `aud`, rejects
  `alg: none`, and returns sentinel errors so callers can log the reason
  without exposing it to clients.
- `transport/ratelimit`: `Store` interface and `MemoryStore` sliding-window
  limiter with injectable clock and idle-key sweep. Shared deployments use the
  Redis implementation below; the interface stays small enough for other
  backends.
- `support/redis`: `Config` validation, `New` (pings within `PingTimeout`),
  `Ping` and `Close` around `go-redis/v9`. No global state, no logger
  overrides, and errors never include the password.
- `transport/ratelimit/redisstore`: Redis sorted-set sliding window behind
  `ratelimit.Store`, using server time and a Lua script so multiple instances
  share one bucket. Backed by `go-redis/v9` and tested with `miniredis`.
- `transport/middleware`: `JWTAuth`, `RateLimit` and `SecurityHeaders`.
  JWT failures return 401/40100; rate limiting returns 429/42900 with
  `X-RateLimit-*` and `Retry-After`, and fails closed with 503 when the store
  errors unless `FailOpen` is set; security headers default to `nosniff`,
  `DENY` and `strict-origin-when-cross-origin`.
- `WithJWTAuth`, `WithRateLimit` and `WithSecurityHeaders`: install those
  middleware on module routes only, so health checks and unmatched paths keep
  working.
- Unmatched paths and unsupported methods now use the shared envelope:
  404/40400 via `NoRoute` and 405/40500 via `NoMethod`. gin writes the `Allow`
  header on 405. `apperr.MethodNotAllowed` is new.
- `transport/middleware`: the access log records the query string with
  sensitive values (`password`, `token`, `authorization`, `api_key`,
  `signature`, ...) replaced by `***`; unparseable queries are logged as
  `***`. Controlled by `AccessLogOptions.LogQuery` and `SensitiveQuery`.

- `logging`: error entries are wrapped in red when the sink is a terminal.
  Files, pipes, `NO_COLOR` and Windows stay plain, so log files never contain
  ANSI escapes.
- `logging`: `system_error_outputs` and `NewSystem` route framework-caught
  panics to a dedicated file. HTTP recovery uses that logger; when the list is
  empty panics keep going to `error_outputs`. Plain file sinks now create their
  parent directory so per-mode log directories work without setup.
- `logging`: `rotation.date_dir` writes files under date directories such as
  `20261106`, and `rotation.mode: hourly` rolls info outputs into
  `info-h0.log` ... `info-h23.log`; `mode: size` keeps lumberjack rotation
  inside the day directory. Error and system-error outputs always use size
  rotation. `date_dir_max_age_days` (default 7, zero also means 7) removes the
  whole expired date directory; `max_age_days` only prunes backups inside the
  current date directory. A `{date}` placeholder in an output path pins the
  date directory before other subdirectories, e.g.
  `storage/log/script/{date}/info/fix-order.log`.
- `examples/demo-service`: logs land in `storage/log/{http,script}/<YYYYMMDD>`
  with hourly `info-h<H>.log`, size-rotated `error.log` and
  `system_error.log`, chosen by `systemenv.IsScriptService()`. Script mode adds
  `info/` and `error/` directories and names files after the script
  (`info/fix-order-h16.log`, `error/fix-order.log`,
  `error/fix-order-system_error.log`) so each script has its own log set.
- `core/systemenv`: `ScriptName` returns the sanitized name after the `script`
  subcommand (whitespace removed, only letters, digits, `-` and `_` kept).
- `core/systemenv`: read-only helpers for the `GOFY_ENV` environment
  (`GetEnv`, `IsDev`, `IsTest`, `IsGrad`, `IsProd`), OS detection and
  `GetGoroutineID`. The package keeps no state and adds no `init()`.
- `core/systemenv`: `IsHTTPService` and `IsScriptService` classify the process
  from its entry subcommand (`start`/`stop`/`restart`/no args vs `script`)
  without caching or package state.
- `start --daemon=1`: re-executes the service in a detached session and exits
  the parent process. `stop` and `restart` still work through the pid file.
  Windows is unsupported and panics with an explicit message.
- `restart` subcommand: stops the instance recorded in the pid file (when
  present), then runs the server in the foreground.
- `support/nacos`: pulls a Nacos config over the Open API into a local
  `config.yaml` at startup, then keeps it current with long polling. The
  generated file is validated as YAML and replaced atomically;
  `WithRestartOnChange` hands the restart to a supervisor after a change.
- `support/nacos.Load("nacos.yaml", "application.yaml")`: the swoolefy-style
  two-file layout. `nacos.yaml` holds the server connection and
  `application.yaml` holds `nacos.service_config`, the change-monitoring
  switch and the listener timings. The tenant resolves from the `nacos.yaml`
  namespace, then the service tenant, then the public namespace.
- `examples/demo-service` uses that pair by default and falls back to
  `nacos.FromEnv()` when `nacos.yaml` is absent. Its `enableNacosSyncConfig`
  constant is the single switch for config sync and watching, so a simple
  project can keep only a local `config.yaml`.
- `App.Reload` / `ReloadSpec`: in-process hot reload of a running app. It
  rebuilds the logger, the MySQL pool and the modules, starts the new modules
  before stopping the old ones, and swaps the HTTP handler atomically while
  the listener, pid file and middleware stay untouched. Injected
  `WithModules` instances are reused rather than restarted.
- `support/nacos` Naming API: `RegisterInstance`, `DeregisterInstance`,
  `BeatInstance` (light and heavy) and `ListInstances`.
- `support/nacos.Registrar` and `gofy.WithNacosRegistration`: registers the app
  as an ephemeral instance once the listener accepts connections, renews the
  lease with the server-provided interval, re-registers after repeated beat
  failures and deregisters on shutdown. Registry settings come from
  `application.yaml` `nacos.service_register` and `nacos.enable_nacos_register`.
- `nacos.Config` now exposes `Register` and `Discovery`, parsed from
  `service_register` and `discovery_service_client`. Discovery defaults its
  namespace and group to the registration values.

### Changed

- Unmatched paths now return the JSON envelope (`code 40400`) instead of gin's
  plain-text `404 page not found`, and unsupported methods return `40500`
  instead of `40400`.
- `stop` and `restart` now ask `你确定 [停止|重启] 应用【X】? (yes or no)` and
  only proceed on `yes`; `--force=1` skips the prompt. Non-interactive callers
  must pass `--force=1`, since EOF is treated as a refusal.
- `start`, `stop` and `restart` print colored status lines and a startup table;
  colors are disabled for non-TTY output, Windows and `NO_COLOR`.
- `examples/demo-service` now reloads in-process when the Nacos config changes:
  its watcher calls `App.Reload` instead of sending SIGTERM, so rebuilt config
  and routes take effect without restarting the process.
- `examples/demo-service` now binds its routes to the `gofy-interface-service`
  `demoservice` contract; the local `app/module/order/dto` package is gone and
  responses use the contract envelope shapes (`order`, `paging.ListData`).

### Fixed

- `Execute` prints usage errors (unknown command, unexpected arguments) to
  stderr before returning, so a flag missing its leading `--` no longer fails
  silently with exit code 2. Script flags report the corrected form
  (`--order-id=1222`), and script `Run` errors reach `App.Logger()` because the
  runtime logger is now exposed on the app.

## [1.0.0] - 2026-11-03

### Added

- `gofy.App` with a single-shot lifecycle: `New`, `Run`, `Handler`, `Execute`
  and idempotent `Shutdown`.
- Typed HTTP handlers with request binding, defaults and `message:"tag:text"`
  validation messages in `transport`.
- Default response envelope `{code,msg,data,req_id,details}` and `apperr` error
  mapping to HTTP status codes and business codes.
- Framework middleware: request id, structured access log, panic recovery,
  CORS and body limits.
- `logging` package with explicit zap configuration, lumberjack rotation and a
  GORM logger adapter.
- `support/mysql` connection management with pool settings, ping and DSN
  redaction.
- `script` and `command` registries, `gofytest` helpers, an optional YAML
  loader with `${VAR}` expansion, and pid-file based `stop`.
- `examples/demo-service`, a standalone module demonstrating the full
  application shape and contract tests.
- Import/`init()` guards, Makefile and a Go 1.26 CI matrix.

### Changed

- The repository is now a library. Applications build their own `main`.
- Removed viper, gogf/gf, go-daemon, robfig/cron, gin-contrib and
  automaxprocs from the framework dependency graph.
- Daemon mode, cron scheduling and implicit signal handling were removed;
  process supervision belongs to systemd or the container runtime.

[Unreleased]: https://github.com/bingcool/gofy/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/bingcool/gofy/releases/tag/v1.0.0
