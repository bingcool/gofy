# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `gofy.WithAccessLogOptions(opts middleware.AccessLogOptions)`: enables the
  access log with an explicit configuration instead of the fixed
  `DefaultAccessLogOptions()`. The supplied options apply literally (no merge
  with the defaults), so `SkipPaths`, `SlowThreshold`, `MaxPathLength`, the
  redacted `SensitiveQuery` list and the field toggles (`LogRequestID`,
  `LogUserAgent`, `LogClientIP`, `LogQuery`) can now be tuned per application;
  nil slices still fall back to the access-log defaults. The middleware keeps
  its position outside `Recovery` and stays registered as `access-log`.
- `support/registry`: generic, explicit name→instance registry for cross-module
  wiring. `NewRegistry` builds a concurrency-safe map; `Register` rejects blank
  names, nil instances (including typed nil pointers/maps/slices) and duplicate
  names without overwriting, `MustRegister` panics for startup assembly,
  `Lookup` returns `(nil, false)` for missing entries (nil registries read as
  empty), `Names` returns a sorted copy for diagnostics, and the generic
  `Get[T]` performs the type assertion with errors that include the name and
  both types. Each assembly (startup or `Reload`) creates its own registry, so
  instances are never shared across generations; unlike
  `support/middleware.Registry` it is untyped and not limited to middleware. It
  is an optional tool for name-indexed or multi-instance wiring — the demo's
  single cross-module service is injected by `app/container` instead.
- `examples/demo-service`: five-package module layering with `innerapi` as the
  only cross-module seam. Every module now has `controller / service / innerapi /
  repository / model`; the intra-module chain is `controller → service →
  repository → model`, and cross-module calls go `order.service →
  product.innerapi` — never service→service or into another module's
  repository/model. `innerapi` is a pure contract package (an interface
  `OrderAPI` / `ProductAPI`, plain views `Order` / `Product` and sentinel errors)
  with no dependency on GORM, the HTTP contract or `app/io`; it carries no
  package-level singleton, publish hook or lookup function.
  `service.Dependencies` is `{OrderRepository, ProductAPI, Logger}`: repositories
  arrive as concrete types, cross-module capacity as the `productapi.ProductAPI`
  interface. Dependency acquisition is a single
  composition root, `app/container`, instantiated once per generation
  (`container.NewContainer()` in `reload.go`). It owns
  `RepositoryDeclare{OrdersRepo, ProductsRepo}` and
  `ServiceDeclare{ProductService, OrderConfigService, OrderService}` — the
  latter one lazy slot per service (`lazy_value.go`): `Init(rt.Logger)` records
  the generation logger, and
  `ProductService()` / `OrderConfigService()` / `OrderService()` build their
  dependency subtree on first access, so a module factory only constructs its own
  module and unused capabilities stay unbuilt. Each slot uses its own `sync.Once`
  plus a `building` flag: re-entering a slot during its own construction means
  the wiring graph has a cycle, which panics with the service name instead of
  deadlocking in `sync.Once`. Wiring is split per module
  (`module_product.go`, `module_order.go`, `module_factories.go`), so the file
  touched by a new dependency stays small as the app grows. Accessor files are
  named `module_<module>.go`, the same convention new modules follow. Module roots keep only `NewModule` / `Register`
  (route binding); per-generation containers replace the former package-level
  singletons, so services are never shared between test cases or reload
  generations, and the order accessor resolves product first, removing the
  factory ordering constraint. Go's import direction (`container → module →
  service`) means the service package cannot reach the composition root, so all
  wiring happens at construction time. The former innerapi service singletons
  (`SetProductService` / `GetProductService`), module-level `RepositoryDeclare`,
  `GetOrderService` / `GetOrderConfigService` getters, `NewModuleFactory` /
  `newModuleFactory`, mode A/B repository resolution, package-level `RepoDeps` /
  `RepositoryDeps` / `a_init_repository.go`, `NameProducts`, the
  `app/repository` composition layer and the `RepositoryRegistrar` interface are
  all gone. Repositories stay
  stateless concrete types (no `OrderRepositoryInterface` /
  `ProductRepositoryInterface`): they carry no connection or logger and resolve
  the `app/io` `db.Db` handle on every query (ctx transaction first), so assembly
  before `app/io.Start` and `Reload` both work and repository logging stays at
  the service via `rt.Logger`. The service returns `innerapi` views and owns the
  domain status constants (`StatusPending/Paid/Cancelled`); the controller maps
  them to the contract DTO/enum (`toOrderDTO`) and a
  `TestStatusMappingConsistency` guards against drift. Errors translate in two
  stages — product `innerapi` sentinels → order domain sentinels
  (`translateProductErr`) → HTTP codes in the controller — so the controller
  never imports another module. The product module owns the real `db.default`
  repository (`FindByID`, `ListByIDs`, `DecreaseStock`) backed by
  `migrations/0002_products.sql`; `DecreaseStock` pushes the status/stock
  conditions into the SQL update and leaves 0 rows to the service to classify.
  Scripts do not run the module lifecycle, so `script/fix_order.go` publishes
  `rt.DB` into the `app/io` handle for the duration of the call. Tests build a
  per-test container over the real repositories on an in-memory pure-Go SQLite
  database (still no external MySQL, still cgo-free), and inject panics through a
  `gorm.ConnPool` wrapper (delegating `BeginTx` to `gorm.TxBeginner`) for the
  recovery case.
- `examples/demo-service`: the product module gained a `ProductService`
  (`app/module/product/service`) that implements `product/innerapi.ProductAPI`
  (get, list-by-ids and stock-decrease), wraps the concrete `ProductRepository`
  and keeps logging at the service boundary. `app/container` passes the instance
  to `OrderService` as the `productapi.ProductAPI` interface, so the order module
  never imports the product implementation.
  `DecreaseStock` classifies a 0-row update into `ErrNotFound` / `ErrUnavailable`
  / `ErrInsufficientStock`; covered by pure-Go SQLite tests.
- `examples/demo-service`: extracted `OrderConfigService`
  (`app/module/order/service/order_config_service.go`) to own the order config
  read use case (currently a fixed sample) and a `Config` domain view. It is a
  standalone service in the same package: the container's
  `OrderConfigService()` accessor constructs it and the module root injects the
  concrete type into `OrderConfigController`, whose `GetConfig` maps the service
  view to `GetConfigResponse` instead of hardcoding the sample. `OrderService`
  does not hold it — no `innerapi` and no registry for intra-module types, which
  are reserved for cross-module calls; config has no cross-module consumer so it
  is not published to `innerapi`. Covered by `order_config_service_test.go` and
  the existing controller/contract tests.
- `support/db`: context-propagated transactions. `Transaction(ctx, db, fn)`
  opens a transaction and stores it on the context passed to `fn`; nested calls
  reuse the outer transaction (no savepoint) and a nil callback or connection
  returns `ErrTxFuncRequired`/`ErrTxDBRequired`. `WithContext(ctx, fallback)`
  resolves the transaction first and only then the explicit connection or app
  handle, so one request can span repositories from several modules. Covered by
  pure-Go SQLite tests for commit, rollback and nested reuse.
- `examples/demo-service`: repositories resolve connections through
  `support/db.WithContext`, and the product repository gained `DecreaseStock`
  (status/stock conditions pushed into the SQL update, 0 rows left to the
  caller). A service-level SQLite test
  (`app/module/order/service/transaction_test.go`) proves stock decrease and
  order create share one transaction and roll back together.
  `shell/check-imports.sh` enforces the five-layer rules per module
  (`controller` → own `service` only, never `app/io`; `service` → no HTTP
  contract, cross-module only the other module's `innerapi`; `innerapi` →
  stdlib/`support` pure types only; `repository` → own `model` only, no contract;
  `model` → no `app/module`/contract/`app/io`), with the module root `module.go`
  exempt as the route-binding layer and `_test.go` files excluded. Wired into the
  demo and root `make guards`/`make check`. `app/container` sits outside
  `app/module`, so it is the only place that imports several modules at once.
- Root package: the App now owns the contract middleware registry. On startup
  and on every `Reload` it creates one `support/middleware.Registry`, registers
  every built-in middleware under its alias (`NameRequestID`,
  `NameHeaderPropagation`, `NameAccessLog`, `NameRecovery`, `NameCORS`,
  `NameBodyLimit`, `NameSecurityHeaders`, `NameRateLimit`, `NameJWTAuth`), then
  runs the new `WithMiddlewareRegistrar` callbacks to add application aliases.
  The registry is handed to the root `support.Router`
  (`support.RouterConfig.Middleware`) and shared by every module group, so
  modules resolve contract names through `Router.Middleware(name)` and no
  longer create or inject registries. `ReloadSpec.MiddlewareRegistrars`
  rebuilds the registry with new configuration, so a changed JWT secret takes
  effect without a restart. A registrar error aborts startup or reload.
- `support/middleware`: alias registry for contract routes. Every built-in
  middleware exports its alias next to its constructor, and `NewRegistry`
  builds an explicit, concurrency-safe alias→instance map: `Register` rejects
  blank aliases, nil middleware and duplicate names (never overwrites),
  `MustRegister` panics for startup wiring, `Lookup` reports missing names (a
  nil registry reads as empty) and `Names` returns a sorted copy for
  diagnostics. The App-level chain installed by `gofy.With*` is unchanged.
- `gofy-interface-service` adapter: every contract middleware name resolves
  through `BindOptions.Middleware` (per-binding override) →
  `BindOptions.Registry` (optional explicit injection) → the router's App
  registry, and unknown names fail at startup. There are no default aliases or
  implicit injection: `Spec.Auth = AuthRequired` is an OpenAPI marker only, and
  protected routes must declare `"auth"` explicitly (`group.Use("auth")` or
  `spec.Middleware = []string{"auth"}`); `BindOptions.Auth`, `AuthAlias` and
  `DefaultAuthAlias` were removed.
- `examples/demo-service`: `app/middleware.Register(reg, Deps{Logger, JWT,
  Responder})` registers `auth`, `audit` and `order-audit` into the App's
  registry; `options.go` wires it with `WithMiddlewareRegistrar` and `reload.go`
  passes the rebuilt registrar through `ReloadSpec`. `app/middleware.Auth` maps
  the top-level `jwt` config (`secret`/`algorithms`/`issuer`/`audience`/
  `require_expiry`) to `jwtauth.Config` and wraps the framework
  `support/middleware.JWTAuth`; an empty secret yields a pass-through
  middleware so the example stays open by default. The order module now only
  binds routes (`BindOptions{}`). The contract declares `"auth"` explicitly on
  the order and config groups. Tests cover the alias set, fresh registries per
  assembly, startup/reload resolution, the audit order inside the contract
  binding, and the JWT policy (40100, valid token, wrong signature).
- `support/image`: image decoding, resizing, thumbnails, cropping, rotation,
  greyscale, image/text watermarks and jpeg/png/gif encoding, ported from the
  swoolefy PHP Image module (Intervention Image) to the standard library plus
  `golang.org/x/image` — no cgo and no GD/Imagick extension. `Manager` validates
  the config once (`max_pixels` defaults to 40M, presets, encode defaults,
  watermark anchors), `Open`/`OpenBytes` pre-check pixels before decoding, and
  `Editor` applies contain/cover/scale/stretch resizes, presets, crops, 90°/any
  angle rotation and greyscale. Watermarks support nine anchors, offsets,
  opacity, TTF text with rotation, and enforce `..` rejection plus
  `allowed_roots` for logo/font paths. Encoding negotiates format as
  global→preset→call, defaults transparent sources to png, validates quality
  and `max_bytes`, and wraps every failure in `errors.Is` sentinels. WebP
  decodes but cannot encode; AVIF is unsupported.
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

- `logger`: error entries are wrapped in red when the sink is a terminal.
  Files, pipes, `NO_COLOR` and Windows stay plain, so log files never contain
  ANSI escapes.
- `logger`: `system_error_outputs` and `NewSystem` route framework-caught
  panics to a dedicated file. HTTP recovery uses that logger; when the list is
  empty panics keep going to `error_outputs`. Plain file sinks now create their
  parent directory so per-mode log directories work without setup.
- `logger`: `rotation.date_dir` writes files under date directories such as
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

- the shell-script directories were renamed from `scripts/` to `shell/` in both
  this repository and `examples/demo-service`, so the name states what they
  hold. `check-imports.sh` moved with them; `make guards` (root `Makefile` and
  the demo `Makefile`), the design docs and the module README now point at
  `./shell/check-imports.sh` and `examples/demo-service/shell/check-imports.sh`.
- `examples/demo-service`: rate limiting is now driven entirely by the
  contract. `app/middleware.RateLimiter` returns the
  `gofyadapter.BindOptions.RateLimit` callback and builds one middleware per
  declared `Spec.RateLimit`: `Limit` / `WindowSeconds` are the effective
  thresholds and `RateKey` selects the bucket subject (`user` via
  `frameworkcontext.UserID`, `api_key` via `X-API-Key`, `route`, or the client
  IP; the identity keys fall back to the client IP when absent). Every bucket
  key also carries the method and route, so one endpoint cannot exhaust
  another's quota. Counting always goes through `support/ratelimit.Store`: each
  route's middleware resolves the Redis alias declared in the contract
  (`RateLimit.RedisAlias`, defaulting to `ratelimit`) and wraps
  `redis.NewIO(ctx, alias)` in `redisstore`, so several instances share one set
  of counters. Resolution is lazy and cached per route, because route binding
  runs before `app/io` opens Redis and a reload must bind the new generation to
  its own connection; failures are not cached. Invalid contract numbers, an
  unavailable Redis instance and counting errors all fail closed with 503
  instead of silently disabling the limit. Routes without `Spec.RateLimit` get
  no limiter. The adapter installs the rate limiter at the front of the route
  middleware chain (after group middleware), so over-limit requests are
  rejected before the route's other middleware and the handler run. The
  composition root keeps a module-agnostic `container.BindOptions()` that only
  hands over the callback. The order contract declares `RateLimit` on all four
  routes, so the generated OpenAPI documents gained their 429 responses.
- Validation failures now promote `message` tag text to the response `msg`:
  matching messages are joined with `; ` when several fields fail and stay in
  `details[field]`, so clients can show `msg` without parsing `details`. The
  tag is resolved through the validator struct namespace, so `dive` item fields
  and embedded structs pick it up too. Requests without a matching tag still
  return `invalid argument`, and the `details` fallback is now a concise
  `field failed on the 'rule' rule (param)` instead of validator's
  `Key: 'Type.field' Error:...` string. The demo contracts (`orderrequest`)
  now ship Chinese `message` tags for every rule.
- `gofy-interface-service`: the shared contract core now lives under `apicore/`
  — `apicore/contracts`, `apicore/client`, `apicore/adapter` and `apicore/cmd`
  — so the directory name marks code that business definitions must not edit;
  `appinterface/` stays the only extensible layer. Import paths, the `refcheck`
  layer prefixes and default directory, the Makefile, CI workflow and README
  moved with it. The demo controller now binds through
  `gofy/gofy-interface-service/apicore/adapter/gofy` and imports
  `apicore/contracts/common/{paging,types}`.
- `examples/demo-service`: the order module binds each contract route group to
  its own handler. `demoservice.Service` gained the narrow `OrdersService` and
  `ConfigService` interfaces (`Service` embeds both), `GetConfig` moved from
  `OrderController` to a dedicated `OrderConfigController` backed directly by
  `service.OrderConfigService`, and `order.NewModule` now takes that config
  service too. `BindGroup` resolves methods per group by `OperationID`, so
  `Register` makes two explicit `BindGroup` calls instead of binding both
  groups to one handler. Existing single-handler implementations are unchanged:
  asserting `demoservice.Service` still works.
- **Breaking:** `WithAPIPrefix` now defaults to empty, so the framework no longer
  prepends `/api/v1` to module routes. A contract route group carries the full
  deployment prefix instead: `orderroute.Orders` and `orderroute.Config` both
  declare `/api/v1/order`, their `Spec.Path` values stay relative
  (`/orders/{id}`, `/config`), and the demo's `order` module returns
  `Prefix() == ""` so no module name is added. The full route is
  `group.Prefix + Spec.Path`. Applications that relied on the implicit prefix
  must declare it on the route group (preferred) or pass
  `WithAPIPrefix("/api/v1")`; tests and documentation were updated to
  `/api/v1/order/...` for the demo and to unprefixed module paths elsewhere.
- **Breaking:** the root logging package moved from `logging` to `logger`.
  Import `github.com/bingcool/gofy/logger` instead of
  `github.com/bingcool/gofy/logging`; `Config`, `New`, `NewSystem`, `Nop`,
  `Sync`, `RotationConfig` and `GormLogger` keep their names, only the package
  qualifier changes (`logger.Config`, `logger.New`, ...). `gofy.LogConfig` is
  now an alias of `logger.Config`, error messages are prefixed `logger:`, and
  the import guards check `./logger`. The package still holds no state and never
  touches `time.Local`.
- `logger`: colored console sinks now render `error` and above as a red
  multi-line block — a header line with time, level, caller, message and the
  remaining fields, one line per `error`/`zap.NamedError` field, and the
  stacktrace expanded frame by frame — instead of one JSON line with escaped
  newlines. The new `log.color` knob (`auto` by default, `always`, `never`;
  invalid values fail `Validate`) decides when it applies: `auto` keeps TTY
  detection, `always` forces it for non-TTY IDE consoles, `never` disables it.
  File sinks are never colored, entries below `error` keep `log.encoding`,
  `NO_COLOR` and Windows still win, and base fields plus `logger.With` fields
  are preserved in the block.
- The request id middleware moved to `support/middleware/req_trace_id.go` and
  now propagates the id through the request context: it reuses a valid upstream
  `X-Trace-ID` (even when a custom header is configured), otherwise generates a
  UUID, stores it via `support.ContextWithRequestID`, and writes it into the
  `headerpropagation` context so `gofy-interface-service/client` forwards
  `X-Trace-Id` downstream automatically. `support.RequestIDFromContext` exposes
  the id to handlers.
- The request id header now defaults to `X-Trace-ID` (previously
  `X-Request-ID`). `support.DefaultRequestIDHeader`, the RequestID middleware,
  the responder `req_id` echo and the interface-service client's
  `RequestIDHeader` all use the same header, so gateways and downstream calls
  keep reusing one id. Callers that still send `X-Request-ID` must switch
  headers or pass an explicit header name to `middleware.RequestID`.
- CORS now follows the swoolefy `CorsMiddleware` semantics: bare domains in
  `AllowOrigins` expand to both http and https, new `AllowOriginPatterns`
  (`*.example.com`) and `AllowPaths` (shell globs over the request URI) are
  supported, requests with a disallowed origin or path get a 403 instead of
  silently continuing, all origin requests receive the full header set (not
  just preflights), headers default to `*` and `MaxAge` to 24h, and any
  OPTIONS request returns 204. Empty origins and patterns still disable CORS
  and `CORS_*` environment variables are not read; both are documented
  differences from the PHP defaults.
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
- **Breaking:** the demo `GetConfig` response no longer returns
  `payment_timeout`; `GetConfigResponse` exposes only `auth_required`. On the
  consumer side `examples/demo-service` drops the `order.payment_timeout`
  config key, the `service.Config`/`Deps.Config` plumbing and its startup log
  field, and the `fix-order` script no longer logs a payment timeout, so
  `script.FixOrder` takes no config argument. The demo OpenAPI golden files
  were regenerated.

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
