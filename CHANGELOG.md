# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `support/phpoffice`: Excel .xlsx export and import ported from the swoolefy
  PHP `PhpOffice\Excel` module, built on the pure-Go `xuri/excelize/v2` instead
  of the xlswriter extension. `Export` validates headers, sheet and file names,
  reserves the target with `O_EXCL` (never overwrites), writes typed rows
  (nil/bool/numbers/text/`time.Time`/`Stringer`), and exposes hooks for sheet
  setup, header style, custom headers, rows finished, filter range and
  pre-save. `ExportStream` writes large exports through excelize's
  `StreamWriter`, keeping memory flat with row count (single header row, no
  hooks or auto filter). `Read` loads every row while `Import` streams through
  the row iterator; both validate the file, sheet, header, row width and cell
  types (`CellString`/`CellInt`/`CellFloat`/`CellTime`), skip empty rows, and
  support row normalization. Exports clean up partial files on failure, and
  every failure wraps a sentinel error for `errors.Is`. The README calls out
  the regular-vs-streaming choice and the Go version required by excelize.
- `support/uuidint`: distributed, trend-increasing `int64` IDs on Redis,
  ported from the swoolefy PHP Uuid library. One Lua script combines a
  millisecond prefix from Redis `TIME` with a segment counter (capped at
  999990), expires the counter keys, and rejects same-millisecond collisions
  for the caller to retry. `New` accepts `WithInstance(name)` to inject a
  `support/redis` named instance (resolved lazily through `NewIO`, so reloads
  are picked up), or `WithClient` for an explicit client; fallback clients or
  instances, TTL, retry count/delay and an error hook are configurable.
  `Get` returns one ID and `GetBatch` a closed ascending range. Go and PHP can
  share the same Redis key because the Lua and ID layout are identical.
  `WithPool` adds an optional per-generator prefetch pool (`StartPool` warms one
  batch synchronously, a ticker refills it, Get/GetBatch consume buffered IDs
  first, and three idle intervals clear the buffer); IDs left in the buffer
  become gaps but never affect uniqueness.
  `examples/demo-service` wires it through `app/io/uuidint` (published as
  `uuidint.Default`, alias and pool from `uuidint.*`) and ships a `gen-uuid`
  script (`--pool --wait`) for running it without the HTTP modules.
- `support/sqlite`: GORM SQLite connections on `github.com/glebarez/sqlite`
  (modernc, pure Go, no cgo; drop-in for `gorm.io/driver/sqlite`, which needs
  `mattn/go-sqlite3`). The API mirrors `support/db` and `support/pg`: validated
  `Config`, `New` pings within `PingTimeout`, `Close`, the same zap-backed GORM
  logger, and the named-instance registry with per-generation close. Pool
  defaults are one connection (`max_idle_conns`/`max_open_conns` = 1) because
  SQLite is single-writer; WAL and `busy_timeout` are enabled through `_pragma`
  DSN parameters. Tests use a temp-dir database, so no external server is
  required.
- `support/pg`: GORM PostgreSQL connections via `gorm.io/driver/postgres`
  (jackc/pgx/v5, no cgo). The API mirrors `support/db`: validated `Config`
  with pool defaults, `New` pings within `PingTimeout`, `Close`, the same
  zap-backed GORM logger, and the named-instance registry
  (`Configure`/`NewRegistry`/`Install`/`NewIO`/`CloseAllIO`) with per-generation
  close. DSN accepts both pgx forms (URL and keyword/value) and error messages
  redact the password in either form. Integration tests run against a real
  server when `GOFY_TEST_PG_DSN` is set.
- `support/encryption`: symmetric encryption aligned with the swoolefy PHP
  Encrypter. AES-128/256-CBC adds an HMAC-SHA256 MAC, AES-128/256-GCM uses the
  AEAD tag, and the base64(JSON{iv, value, mac, tag}) payload is byte-compatible
  with PHP, so `EncryptString`/`DecryptString` interoperate across languages
  (verified with fixtures in both directions). `NewEncrypter` validates the
  raw-byte key length and defaults to aes-256-gcm; `Encrypt`/`Decrypt` add JSON
  serialization for values. CBC verifies the MAC before decrypting, and keys
  are copied at construction and never exposed.
- `support/otel`: production OpenTelemetry tracing with a validated `Config`,
  OTLP gRPC/HTTP exporters, batch span processing, TLS or explicit insecure
  transport, gzip compression, parent-based ratio sampling and W3C
  TraceContext + Baggage propagation. Export errors go to the zap logger and
  are rate-limited, and shutdown flushes within `ShutdownTimeout` without
  failing the process when the collector is unreachable.
- `support/otel` instrumentation helpers: `GinMiddleware` (extracts
  `traceparent`, skips probe paths), `HTTPTransport`/`HTTPClient` (injects
  `traceparent` on outbound calls), `GORMPlugin` (SQL template and rows
  affected, never bound values), `InstrumentRedis` (no command arguments) and
  `Logger`/`TraceFields` for trace-correlated logs.
- `gofy.WithTelemetry` and `gofy.TelemetryConfig`: enable tracing from the App
  lifecycle. The App installs the Gin server span middleware, instruments the
  MySQL pool it owns, exposes `Runtime.TracerProvider`, `Runtime.Propagator`
  and `Runtime.Tracing`, and shuts the exporter down during cleanup. Service
  name, version and environment default to the App name, build version and
  environment.
- Access and recovery logs now include `trace_id` and `span_id`, and
  `X-Trace-Id` uses the OTel trace id when tracing is enabled, falling back to
  the request id otherwise.
- `support/headerpropagation`: captures a whitelist of request headers
  (`X-Trace-Id`, `X-User-*`, `X-Tenant-Id`, `X-Client-Ip`, `X-User-Agent`) and
  carries them in the request context. Identity headers are trusted only when
  the direct peer matches the configured proxies; plain clients cannot spoof a
  tenant, and credentials such as `Authorization` never propagate.
- `WithHeaderPropagation` and `support/middleware.HeaderPropagation`: install
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
- `support/cache`: a `Cache` facade over an injected Redis client. It covers
  `Set`/`Get`/`Delete`/`Has`/`TTL`, batch `SetMany`/`GetMany`, and
  `SetJSON`/`GetJSON` helpers. `ttl <= 0` means no expiration, matching the
  swoolefy library; the package never owns or closes the client.
- `support/queues`: Redis-backed immediate and delayed queues over an injected
  client, matching swoolefy's key layout. `Queue` offers FIFO, priority and
  retry (`{key}`, `{key}:priority_queue`, `{key}:retry_queue_sort`), while
  `DelayQueue` stores due timestamps in a ZSET. Due retries are promoted and
  due delayed messages are claimed with Lua scripts, and messages use an
  envelope with `__id`, `__retry_num`, `__timestamp` and a nested `payload`.
- `support/amqp`: AMQP 0-9-1 (RabbitMQ) clients and queues. `Config` lists
  multiple hosts with ordered fallback, `New` dials them and reconnects on
  demand, and `Wrap` injects an existing connection. `Queue` declares
  direct/fanout/topic/headers topologies, publishes with optional publisher
  confirms, and consumes with prefetch, automatic ack/nack (requeue or
  discard), panic recovery and reconnect. `DelayConfig` adds TTL + dead-letter
  delayed messages, declaring the target topology before the staging queue.
  Multi-instance wiring stays in the application, as with Redis.
- `support/kafka`: a pure-Go Kafka (franz-go) client, producer and consumer
  group. `Config` lists seed brokers, `New` discovers the cluster through
  metadata and `Wrap` injects an existing client. `Producer` publishes
  synchronously (`ProduceSync`) and keeps per-key ordering; `Consumer` polls a
  group with configurable auto/manual commit, earliest/latest offset reset,
  session timeout and rebalance callbacks. Handler errors default to
  redelivery (at-least-once) with `DiscardOnError` for poison messages, and
  panics are recovered into errors. Multi-instance wiring stays in the
  application, as with AMQP.
- `support/rocketmq`: an official `apache/rocketmq-clients/golang/v5` client
  (`v5.1.2`) over the gRPC proxy (default 8081), so no sidecar and no name
  server. `New` starts a producer; setting `StartupTopics` (and leaving
  `SkipStartupCheck` false) optionally probes their routes to fail fast.
  `Client.Publish`/`PublishJSON` send synchronously with tags, indexed business
  keys, properties, an absolute `DeliverAt` delay and a `MessageGroup` for FIFO
  topics. v5 has no push consumer, so `NewConsumer` builds a `SimpleConsumer`
  and `Consume` runs a long-poll loop with per-message acknowledgement, tag and
  SQL92 filters, a configurable batch size, concurrency and invisible
  duration, and at-least-once redelivery when a handler fails. Consumers
  require a group; producers no longer register one, so duplicate-group
  validation is gone.
- `support/db`, `support/redis`, `support/amqp`, `support/kafka`,
  `support/rocketmq`:
  `Configure(map[string]Config)` registers named configurations and
  `NewIO(ctx, name)` returns the cached instance for an alias, opening it on
  first use. Each `Configure` call starts a new generation whose `Close` only
  releases its own connections, so a reload can start new modules before the
  old ones stop. `NewRegistry` + `Install` split that into "build" and "make
  current" so a failed startup can be discarded without replacing the working
  generation. Unregistered names report `ErrUnknownInstance`; using `NewIO`
  before `Configure` (or after `Close`) reports `ErrNotConfigured`. Packages
  keep their existing `New`/`Close` behavior when `Configure` is not used.
- `config.LoadDocument`: reads a YAML file into a `Document` so callers can
  fetch individual values by path (`GetString("db.default.dsn")`,
  `GetInt`, `GetBool`, `GetDuration`, `GetStringSlice`, ...) without declaring
  a full struct. It expands `${ENV_VAR}` like `Load` and keeps `Load` unchanged
  for typed decoding with defaults and validation.
- `support/jwtauth`: HMAC JWT verification for HS256/HS384/HS512. It validates
  signature, `exp`/`nbf` with optional leeway, `iss` and `aud`, rejects
  `alg: none`, and returns sentinel errors so callers can log the reason
  without exposing it to clients.
- `support/ratelimit`: `Store` interface and `MemoryStore` sliding-window
  limiter with injectable clock and idle-key sweep. Shared deployments use the
  Redis implementation below; the interface stays small enough for other
  backends.
- `support/redis`: `Config` validation, `New` (pings within `PingTimeout`),
  `Ping` and `Close` around `go-redis/v9`. No global state, no logger
  overrides, and errors never include the password.
- `support/ratelimit/redisstore`: Redis sorted-set sliding window behind
  `ratelimit.Store`, using server time and a Lua script so multiple instances
  share one bucket. Backed by `go-redis/v9` and tested with `miniredis`.
- `support/middleware`: `JWTAuth`, `RateLimit` and `SecurityHeaders`.
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
- `support/middleware`: the access log records the query string with
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

- **Build guarantee:** the whole module is cgo-free. All packages build with
  `CGO_ENABLED=0`, and the dependency graph has no `CgoFiles` (SQLite uses
  `glebarez/sqlite`/modernc instead of `mattn/go-sqlite3`). `make cgo-free` and
  the CI guards enforce this; `-race` still runs with cgo enabled.
- **Breaking:** `support/mysql` is now `support/db`, positioned as the GORM
  connection package for any MySQL-protocol server (MySQL, MariaDB, TiDB,
  OceanBase, SelectDB/Doris). Update imports to
  `github.com/bingcool/gofy/support/db` (suggested alias `gofydb`) and error
  prefixes from `mysql:` to `db:`. `gofy.DatabaseConfig` is unchanged. The demo
  moves `app/io/mysql` to `app/io/db`; `Open` no longer rejects unknown
  instance names — `order`/`product` still open eagerly with tracing, while
  any other name (e.g. `selectDb`) is registered for lazy `NewIO` opening and
  is not auto-instrumented.
- `App.Reload` keeps the OpenTelemetry provider and exporter built at startup;
  changing telemetry settings requires a process restart.
- **Breaking:** the `transport` package is now `support`. `Router`, typed
  handlers, binding, `Responder` and the CORS/JWT/rate-limit/security-header
  configs live in the `support` package; middleware moved to
  `support/middleware` and the limiter to `support/ratelimit`. Update imports
  from `github.com/bingcool/gofy/transport...` to
  `github.com/bingcool/gofy/support...` and `transport.X` to `support.X`.
  Config errors now use the `support:` prefix.
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
  validation messages in `support`.
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
