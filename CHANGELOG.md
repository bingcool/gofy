# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- `support/consumer`: the processing lease is now written and compared in
  database time. `updated_at` came from GORM's application-clock timestamps
  while the expiry test ran in the consuming process, so a node whose clock ran
  fast could take over a message another node was still handling, and a slow
  node could hold a message hostage. Both sides now use `CURRENT_TIMESTAMP`
  (each dialect's own interval spelling for the comparison), and the takeover
  re-checks the lease inside its CAS. Dialects without an expression fall back
  to the node clock, which the README now states as requiring NTP.
- `gofy` worker mode: `--daemon` now waits for the child to report readiness
  instead of treating `fork` as success. The detached child's stdout is
  `/dev/null`, so a database that would not open, a health port already taken or
  a broker that rejected the subscription all looked like a successful start.
  The child gets a pipe on descriptor 3 and writes one line — `ready` after the
  whole group reports ready, or `error: ...` before it exits — and the parent
  prints success only after reading it. A child that dies silently is reported
  as such; a child that misses `--startup-timeout` (default 30s) is reported as
  unconfirmed but left running, because a slow start is not a failed one.
- `gofy` worker mode: two processes on the same host can no longer own one
  group. The startup path read the pid file, checked the pid and wrote its own —
  a check-then-write window in which two starts both saw "no live process" and
  both went on to consume. Ownership is now claimed through a
  `<pid file>.lock` file (`flock` on Unix, `LockFileEx` on Windows), which the
  kernel grants to one process and releases on exit, SIGKILL included. The pid
  file keeps its job as the address `worker stop` uses.
  Scope is one host: the lock sits on the local filesystem, so two pods with
  separate filesystems still need a replica limit or a distributed lease.
- `support/kafka`, `support/amqp`, `support/rocketmq`, `support/queues`: each
  `ConsumeOptions` gains `OnReady`, fired once when the subscription is really
  usable — the first clean poll (Kafka), after the topology is declared and the
  delivery stream exists (AMQP), after the SDK consumer started (RocketMQ), and
  after the first successful pop (Redis queues). `support/consumer` reports
  worker readiness from there instead of before it starts the consume loop, so
  a queue that cannot be declared, a topic the group may not read, or a broker
  that refuses the subscription leaves `/healthz` at 503 instead of green.
  Delayed consumers now implement `worker.ReadyReporter` too; they had been
  reporting ready merely by entering `Run`.
- `support/worker`: readiness is counted per instance, not per signal. The gate
  kept a single counter, so an instance that reported twice — an adapter
  reconnecting inside one Run, or the runner restarting it after an error —
  subtracted a sibling's pending slot and the group could report ready while
  another instance had not connected yet. Signals are now keyed by the identity
  the runner injects, duplicates are ignored, and the registered keys are built
  from the same list the goroutines carry.
- `support/consumer`: a failed dead-state write no longer lets the broker
  acknowledge the message. `park` used to log the failure and return nil, so a
  permanent failure whose ledger row could not be written was ACKed anyway —
  the broker deleted its only copy and nothing was left to investigate. The
  dead state must now be durable before the message is acknowledged; a write
  failure comes back as a retryable error and each transport holds the message
  (Kafka does not commit, AMQP nack-requeues, RocketMQ does not ack, the Redis
  queue re-queues).
- `gofy` worker mode: `worker restart` no longer re-runs itself in the daemon
  child. The detached process used to inherit `os.Args`, so it executed
  `worker restart` again, stopped nothing, and returned at the confirmation
  prompt with stdin on `/dev/null` — the parent had already printed
  "started in the background" while the service stayed down. The child now
  receives the start form rebuilt from the parsed options
  (`worker --group=... [--name=...] --daemon=false`), which drops `--force`,
  `--timeout` and the subcommand itself.
- `gofy`: a reload can no longer pair handler N with module state N+1. Modules
  that own generation-scoped state implement `support.RequestBinder`; the
  framework composes their binders into the generation it publishes and applies
  them after the generation lease is acquired, right before the handler runs.
  `examples/demo-service/app/appio` now binds its IO snapshot this way, script
  entries pin the snapshot they started, and `generation.Set.Middleware` no
  longer overwrites an existing pin. A reload that fails before publishing
  leaves the serving generation's state untouched.
- `support/claim`: `Succeed`, `Fail` and the dead transition now compare
  `attempts` as well as `owner` and `status`. Owner alone could not fence a
  re-claim by the same worker instance after a reap; the stale write now fails
  with `ErrLeaseLost`, and a task built by hand fails with `ErrInvalidTask`.
  `worker.Identity.Owner()` renders `group/name#index@host`, which is the value
  to claim with — `String()` stays host-free for log and metric labels.
- `support/consumer`: the idempotency ledger is a real state machine. A
  redelivery that finds another instance still inside its processing lease is
  answered with `ErrInFlight` (retryable, **not** acknowledged) instead of being
  let into the handler; an expired lease or a recorded `failed` may be taken
  over with a new attempt. `succeed` / `fail` / `park` are fenced by that
  attempt and reject `done` / `dead`, so a late handler cannot rewrite a
  finished message. `ProcessorConfig.ProcessingLease` (default
  `DefaultProcessingLease`, 5 minutes) sizes the lease, which reuses
  `updated_at` and adds no column.
- `gofy` worker mode: `/healthz` now reports readiness only after every
  instance of every group reported that it can accept work. The four message
  consumers implement `worker.ReadyReporter` and call `worker.Ready(ctx)` once
  the connection and subscription exist (Kafka pings the broker first); polling
  workers do not participate and stay immediately ready. A start that fails
  never closes `Runner.Ready()`. The probe also flips to 503 the moment the
  drain starts instead of after the last runner returned.
- `gofy-interface-api`: `WithQuery` no longer panics. `newCallOptions`
  initializes `query`, so ordinary calls, repeated keys and the streaming
  helpers all accept the option.
- `gofy`: the drain gate is now an admission gate. The open/closed check and
  the generation lease acquisition happen under one read lock, so once
  `StopAccepting` returns no further request can enter a handler — the previous
  check-then-acquire left a window where a request that read "open" still got
  in. `ServeHTTP` also releases the lease with `defer`, so a handler panic
  (`http.ErrAbortHandler`, or any panic with Recovery disabled) no longer leaves
  the counter elevated and adds a drain timeout to every later reload.
- `gofy`: a failed `restart` / `exit` no longer leaves the process alive,
  deregistered and answering 503. `StopAccepting` keeps the registrar reference,
  `App.ResumeAccepting` reopens the gate and republishes the instance, and
  `nacos.WithResume` calls it when `exec` or the termination signal fails. If the
  resume itself fails the watcher asks for termination, so a supervisor can
  recover instead of the process becoming a zombie.
- `gofy`: a config change that arrives while the process is still registering in
  Nacos can no longer leave it advertised but drained. Registration is checked
  against the gate afterwards and withdrawn immediately; failing to withdraw
  fails startup.
- `gofy-interface-api`: `StreamSSE` re-resolves the base URL on every reconnect,
  so a Resolver can move the stream to another instance instead of retrying the
  endpoint that just dropped. A fixed `BaseURL` still returns the same address,
  and `Last-Event-ID` is carried across the switch.
- `gofy-interface-api`: the `WithConnectTimeout` derived-client cache is bounded
  (`maxDerivedClients`, 32) and now has a lifecycle. Entries beyond the cap still
  serve the call but are not kept, and `Client.Close` releases the idle
  connections of the pools this package created while leaving a
  `Config.HTTPClient` alone.
- `gofy-adapter`: `k8slease.Locker` is single-flight per key within the process.
  Re-acquiring a lease the same identity already holds still keeps the token —
  that rule exists for a *restarted* pod, not for concurrent callers — but a
  second `Acquire` in the same process now returns `ErrHeld` instead of silently
  joining the same term. `Release`, and a `Renew` that discovers another holder,
  free the reservation.
- `support/worker/consumer/rocketmqconsumer`: a single-use consumer could not
  survive a Runner restart — the second `Run` consumed from a client that had
  already been consumed. `NewFactory` builds a fresh consumer per `Run` and
  always closes it; `New` keeps the one-shot contract and says so.

### Changed

- `support/claim`: the task column `job` is now `job_type` (the Go option stays
  `Job`, so callers are unaffected), and a `fail_reason` TEXT column records the
  bounded cause on `Fail` and is cleared on `Succeed` — a dead row now explains
  itself. Three `DATETIME` columns now record the last relevant event:
  `start_handle_time` on Claim (status 1), `finish_handle_time` on Succeed (2),
  and `fail_handle_time` on every failure — a retryable Fail returning the row
  to pending (0), Fail at the attempt cap, and Reap on an expired lease all stamp
  it. Lease and retry deadlines
  are rounded up to whole seconds in Go before reaching the plain DATETIME
  columns; without that, MySQL could round a sub-second backoff down to zero and
  retry immediately. Both the DDL string and the demo migration carry the same
  shape.
- `support/claim`: new `Runner` layer wraps the whole poll round (Reap → Claim →
  Handler → Succeed/Fail → hooks) so a worker supplies only a `RunnerConfig`, one
  `Handler func(ctx, data []byte) (taskID uint64, err error)` and optional
  `OnSuccess`/`OnFailure` hooks carrying the full `Result` (task/row id, attempts,
  elapsed, err, dead, leaseLost). `err == nil` closes the row via Succeed;
  `err != nil` records the reason and schedules a retry or parks as dead. An idle
  round calls the Handler once with `data == nil` (return values ignored). Two
  nil hooks are normalized to no-ops. `claim.sql` is a copy-paste migration for
  `claim_job_task`, kept byte-identical to `DDL` by a guard test.
- `support/claim` DDL and the demo's `job_task` migration: every column now has
  a COMMENT, `created_by` and `deleted_at` were added as application-owned
  columns, and the time columns are plain `DATETIME` rather than `DATETIME(3)`.
  Claim never reads or writes the two new columns — a non-NULL `deleted_at` does
  not hide a row — and second precision rounds any lease or backoff below one
  second, which neither the demo nor claim's defaults use.
- `examples/demo-service`: the order consumer now enforces the state machine
  instead of only skipping a same-status write. `order.paid` may move pending to
  paid, `order.cancelled` may move pending or paid to cancelled, and no event
  moves a cancelled order anywhere — a late `order.paid` for a cancelled order
  used to write the status back to paid. The allowed sources live in the UPDATE
  (`status IN (...)`), so two concurrent handlers cannot both pass a read; zero
  rows is then split into "not created yet" (retry), "already there"
  (idempotent), and "stale event" (acknowledged with a warn line), which
  idempotency alone could not tell apart.
- `examples/demo-service`: the command line is classified before any file is
  opened, so a command only reads what it needs. `version` and `--help` read
  nothing, `stop` / `worker stop` read `config.yaml` for the pid path but never
  `nacos.yaml`, `worker --list` reads the config but skips Nacos, and only the
  commands that start a process pull the remote config. `worker restart` counts
  as a start: it pulls and validates the config **before** the old process is
  stopped, so a failed pull leaves the running instance alone instead of
  replacing it with one on stale settings. Control commands also build a minimal
  App — no database, modules, middleware, telemetry or log files — because
  sending a signal must not depend on any of them.
- `internal/pidfile`: the pid file is now a one-line JSON record — pid,
  executable, claim time and a process identity token — instead of a bare
  integer. A pid alone cannot be trusted after a crash because the kernel
  recycles numbers: `worker stop` would signal whatever process inherited it.
  The token is derived from the process start time (boot id + `/proc` starttime
  on Linux, `kern.proc.pid` on macOS, creation FILETIME on Windows), so a reused
  pid can never reproduce it. Stop verifies the token before signalling and
  treats a mismatch as "not running", removing the stale file; legacy files with
  no token are still accepted and fall back to the liveness check with a logged
  warning.
- `examples/demo-service`: the order module's `auth_required` now comes from the
  `order:` section of `config.yaml` instead of a constant in the service. The
  section travels as a constructor argument (`config.OrderConfig`), so the
  service sees its own section and never the whole application config;
  `check-imports.sh` allows that import from `service` only and keeps rejecting
  it for controller, repository, query, model and innerapi. Adding a field is two
  edits (config type, service body) and does not touch unrelated modules.
- `gofy` worker mode: `--group=all` selects every registered group. The wildcard
  `*` still works but a shell expands it before the process starts — zsh refuses
  to run the command at all (`no matches found`), bash only passes it through
  because nothing matched — so the alias is the spelling that survives every
  shell. `worker --list` and the missing-`--group` error both point at it.
- `gofy` worker mode: `stop --worker=*` now stops every worker process instead of
  only the pid file the wildcard run wrote. Groups are often started as separate
  processes, and stopping "all" while leaving those running was the wrong answer.
  `worker stop --group=<g>` remains the single-group form, identical to
  `stop --worker=<g>`.
- `gofy-adapter`: README documents the Kubernetes Lease adapter — minimal RBAC,
  Lease naming, identity uniqueness, why the object must not be deleted (the
  fencing token resets), and the single-flight rule. The import guard now rejects
  gin and the gofy root package inside that adapter, which keeps it usable from a
  worker-only process.
- `examples/demo-service`: the order query returns `MaxOrderID` / `max_order_id`
  instead of `LastOrderID`. `MAX(id)` is the largest primary key, not the most
  recently created order, and the old name implied the latter.

### Added

- `examples/demo-service`: `group1` gains a working claim queue. A migration
  (`migrations/0005_job_task.sql`) declares `job_task` in the shape
  `support/claim` expects, `order-settle-producer` enqueues a batch of rows in one
  transaction every ten seconds — five due now, one due in fifteen, plus one that
  is meant to fail every third round — and `order-settle-consumer` claims them
  from two instances with `FOR UPDATE SKIP LOCKED`, a thirty-second lease and a
  three-attempt budget. The sample shows the four outcomes on purpose: settled,
  retried after a two-second exponential backoff, parked as dead, and a row whose
  `next_run_at` keeps it invisible until it is due. `scan.go`'s comment now
  points at it instead of describing a queue that did not exist.
- `examples/demo-service`: `group1` gains a RocketMQ batch-pull subscription and
  the burst producer that feeds it. `batch_size=10` makes one `Receive` return a
  batch; the handler is still per message and each message is acknowledged on
  its own, so one failure costs one redelivery instead of the whole batch — and
  the handler never sees a batch boundary, which is why the sample explains when
  a batch cannot become one transaction. The burst producer sends its ten
  messages every ten seconds with a distinct envelope id each: the ledger is
  keyed by `(consumer, message id)`, so a shared id would turn nine of them into
  `message skipped`.
- `support/lock`: Redis-backed distributed mutexes on top of redsync
  (`github.com/go-redsync/redsync/v4`, go-redis v9 adapter). `Lock` retries per
  policy and reports `ErrAlreadyHeld`; `TryLock` makes exactly one attempt;
  `Unlock` deletes the key only while it still holds this mutex's token, so an
  expired lock that another owner took over is never released by its previous
  holder; `Extend` renews the TTL and reports `ErrNotHeld` once the lock is
  gone. A busy lock, a Redis failure and a canceled context are three distinct
  errors, so a shutdown is never logged as contention. `lock.Config` +
  `lock.Open` take the YAML shape (`enabled`, `redis_instance_name`, `prefix`,
  `expiry`, `tries`, `retry_delay`); `enabled` has no default so a missing
  section cannot start writing keys. See `support/lock/README.md`.
- `examples/demo-service`: `app/appio/redislock` publishes the lock as part of
  the io generation (`appio.WithContext(ctx).RedisLock()`), bound to that
  generation's Redis client, with the `lock:` config section; `script lock-check`
  acquires/holds/releases a key, and `--try` shows the conflict path.
- `gofy` worker mode: `worker --daemon=1` detaches into its own session and exits
  the parent, the same way `start --daemon` does. The child appends
  `--daemon=false`, so it runs in the foreground of that session; the parent
  checks the pid file first, because the child's stdio goes to `/dev/null` and a
  duplicate would otherwise die silently after the parent reported success.
- `gofy` worker mode: `worker stop` and `worker restart` subcommands, sharing
  `--group` with the run form. Without `--group`, `stop` stops every worker that
  left a pid file and `restart` brings the group back as one wildcard process;
  `restart` defaults to `--daemon=true`, since a service that ran detached has to
  come back detached (`--daemon=false` keeps it in the foreground).
- `support/worker/consumer/rocketmqconsumer`: `NewFactory(name, factory, cfg)`
  and `ConsumerFactory`, for the Runner restart path.
- `gofy`: `App.ResumeAccepting(ctx)` and `nacos.WithResume(fn)`, the counterpart
  of the drain hook.
- `gofy-interface-api`: `Client.Close()`.
- `examples/demo-service`: new read-only `query` layer in the order module
  (`app/module/order/query`) for raw SQL, joins and `EXISTS` / `NOT EXISTS`
  reads. Each module can carry its own query package; it resolves its connection
  through `appio.WithContext` + `gofydb.WithContext`, so a query shares the
  caller's transaction and the request-pinned generation instead of opening a
  second path to the database. Samples cover a three-table inner join, a
  `NOT EXISTS` anti-join, an `EXISTS` probe and a grouped aggregate with
  `HAVING`; `migrations/0004_order_items.sql` adds the backing table with demo
  rows. The import and io-access guards cover the new layer.
- `examples/demo-service`: `worker --group=<g>` now pulls the Nacos config once
  at startup but never watches it. A freshly launched worker starts on the
  current remote config rather than a possibly stale config.yaml baked into the
  image, while staying immutable at runtime — config changes reach workers
  through a rolling restart by the deployment layer. Query commands
  (`worker --list`, `script`, `stop`, `version`) still never contact Nacos.
  Docs now state that the three change strategies apply to the HTTP process
  only, and that `restart` is for hosts without an external supervisor.
- `support/nacos` / `gofy`: config changes using the `restart` or `exit`
  strategy now quiesce before acting. `nacos.WithDrain` injects a hook and
  `App.StopAccepting(ctx)` refuses new HTTP requests with 503 (closing the
  connection) and deregisters from Nacos naming, while in-flight requests keep
  their generation lease and the listener stays open. The hook must not call
  `App.Shutdown` — it runs inside the watcher goroutine and would self-join.
  The new `restart_grace_period` (application.yaml, seconds, or
  `NACOS_RESTART_GRACE_PERIOD`) is a fixed, ctx-cancellable pause between
  quiescing and exec/SIGTERM. Like the strategy it is environment-over-file.
  Default `0` keeps the previous immediate action; the pause only lowers the
  chance of cutting work in half, it does not join in-flight tasks.
- `examples/demo-service`: wires `reloader.stopAccepting` as the drain hook and
  sets `restart_grace_period: 5`.
- `support/nacos`: `config_change_strategy` (application.yaml, or
  `NACOS_CONFIG_CHANGE_STRATEGY`) selects what happens after a changed config
  is written. `reload` (default) calls `WithOnChange` so an app can run
  `App.Reload` in place; `restart` re-executes the binary with `syscall.Exec`
  for hosts without a supervisor; `exit` sends SIGTERM and leaves the restart
  to Kubernetes, a Docker restart policy or systemd. Strategy is the one key
  where the environment overrides the file, so an image that says reload can
  still be deployed as exit. Unknown values fail at Load rather than silently
  falling back to reload. `WithRestartOnChange` stays as the compatibility
  form of exit.
- `examples/demo-service`: the Dockerfile now ships nacos.yaml and
  application.yaml, gives the non-root user a writable /app (Sync replaces
  config.yaml atomically and the pid file lives in app/), and defaults
  `NACOS_CONFIG_CHANGE_STRATEGY=exit` so an orchestrator restarts the process
  on a config change. The demo's application.yaml defaults to reload for
  local development.
- `examples/demo-service`: `uuidint.fallback_redis_instance_name` optionally
  names a backup Redis instance. `support/uuidint` already had
  `WithFallbackClients` / `WithFallbackInstances`; the demo now wires a second
  per-generation client through `app/appio` and `script gen-uuid`, so an outage
  of the primary still yields IDs. The option is empty by default, must name an
  enabled instance different from the primary, and fails at startup when that
  instance has no client instead of degrading to a package-level alias.
- `support/uuidint`: `WithFallbackDelay` pauses after the primary exhausts its
  retries and before the first fallback attempt (default 10ms, interruptible by
  ctx, skipped when no fallback is configured). IDs combine a millisecond
  prefix with a counter that never carries, so an independent Redis with its
  own counter can only collide inside the primary's last millisecond; the pause
  moves the handover past that boundary. It does not correct a clock offset
  between the two servers, nor restore trend-increasing order across a handover.
- `support/consumer`: a resident consumer is now "one subscription, one file".
  `NewKafka`, `NewAMQP`, `NewRocketMQ` and `NewQueues` take a config struct and
  a `Connections` table and return a value that satisfies `worker.Worker` and
  `worker.Concurrent`, so a group registers a consumer next to its polling loops
  and writes no adapter of its own. Each config carries its own defaults —
  `Concurrency` 1, `MaxAttempts` 5, Kafka `CommitMode` manual, AMQP
  `PrefetchCount` 32, RocketMQ `AwaitDuration` 5s, Redis `MaxRetries` 3 — and a
  `validate` that rejects a missing `Topic`/`Group`/`Queue`/`Key`, a bad
  `CommitMode` or `ExchangeType`, a direct exchange whose routing and binding
  keys differ, a RocketMQ tag that is empty or carries the `||` separator the
  SDK builds its expression with, and a concurrency outside 1..255.
  Construction resolves the instance name but never connects; `Run` connects,
  consumes and releases, so `worker --list` and a `--name`-filtered worker cost
  nothing. A missing instance fails at registration instead of starting a
  consumer that reads nothing.

  Kafka adds `AutoCommitInterval` and `BlockRebalanceOnPoll`. The second stands
  in for `max.poll.interval.ms`: franz-go heartbeats on its own goroutine, so a
  slow handler never drops the member out of the group, and the window left to
  close is a rebalance landing between the handler and its commit. With the
  flag set, the batch finishes and commits before the rebalance proceeds; it is
  off by default because a handler that never returns would hold the group.

  Handlers receive the subscription's own config, the decoded `Envelope` and
  the native broker entity — `KafkaMessage.Record`, `AMQPMessage.Delivery`,
  `RocketMQMessage.Message`, `QueuesMessage.Message` — which is what a manual
  commit, an explicit `Ack` or an offset log line needs, and what lets one
  handler serve several subscriptions without closing over the consumer.
  Behind them, `NewProcessor` continues the
  producer's trace from `traceparent`, claims `(consumer, message_id)` in the
  `worker_consumed_messages` ledger with a single insert (no lock), decodes the
  `id`/`type`/`occurred_at`/`payload` contract, and splits permanent failures
  (`Permanent(err)` is parked `dead` and acknowledged) from retryable ones (back
  to the broker until `max_attempts`).

  This replaces the `worker.yaml` mechanism: one block per broker could only
  describe one subscription per process, which is the wrong shape for a service
  that reads several event streams. Consumer settings now live next to the
  handler that uses them, and `config.yaml` keeps only the broker connection
  blocks. `examples/demo-service/worker` is the reference: four one-file
  subscriptions plus the shared order handler.
- Contract modules are now free of the gofy framework dependency: the
  `internal/integration/**` tests moved to `examples/demo-service`, so
  `examples/interface-service/go.mod` only requires `gofy-interface-api`
  (one local replace). `gofy new interface-service` now needs only the
  sibling api library checkout and embeds a 43-file template snapshot.
- `gofy new interface-service`: scaffolding for a project contract module.
  The command embeds a 43-file snapshot of `examples/interface-service`, rewrites the
  module path and every import, points the single local replace at the sibling
  `gofy-interface-api` checkout with a portable relative path, runs `go mod tidy` and
  initialises git so the generated project's first command can be `make check`.
  `make sync-contract-template` regenerates the embedded snapshot.
- `support/generation`: the publish-and-pin mechanism behind the demo's IO
  snapshot is now a framework package. `generation.NewSet[T](name)` keeps one
  current generation in an `atomic.Pointer`, `Publish`/`Current` swap and read
  it without locks, `CompareAndSwap` lets a stopped module withdraw only its own
  generation, `WithSnapshot` pins a generation to a context and `WithContext`
  resolves pinned-first with a `Current()` fallback, panicking with the set name
  when neither exists. `Middleware` pins the entry generation for gin, and
  several sets in one process stay isolated. `examples/demo-service/app/io` now
  builds `currentSnapshot = generation.NewSet[Snapshot]("app/io")` and its
  `Current`/`Publish`/`WithSnapshot`/`WithContext`/`Middleware` delegate to the
  package, making the demo a usage example rather than the only implementation;
  `Module.Stop` withdraws its generation through `CompareAndSwap`.
- `logger.Config.AccessOutputs` (`access_outputs`) and `logger.NewAccess`:
  HTTP access entries can be written to their own outputs instead of the
  application logger. `NewAccess` accepts Info and above (access entries pick
  Info/Warn/Error by status and latency, so `Level` does not filter them) and
  follows `Rotation.Mode` and `DateDir`; an empty list returns a nop logger and
  the App falls back to the main logger, keeping the previous behaviour. The
  App builds, syncs, retires and rebuilds this logger alongside the application
  and system loggers across `Reload`, and the access-log middleware now writes
  to it. `examples/demo-service` writes `storage/log/access/<date>/access.log`
  in HTTP mode; runtime logs stay in `storage/log/http/<date>/`, and script
  mode has no access log.
- Generation lease and reload drain: the HTTP handler now swaps between whole
  generations instead of a bare router. Each request acquires a lease on the
  generation it entered; `Reload` publishes the new generation first, then
  retires the old one, whose modules and resources are released only after its
  in-flight requests finish or `WithShutdownTimeout` (default 10s) expires.
  Requests racing a retire retry on the successor (a persistent race answers
  503 rather than entering a shutting-down generation), and a failed
  new-generation start discards only the resources that generation opened.
  `Handler`/`Run`/`Execute` keep serving from the same listener.
- `examples/demo-service`: request-level IO snapshot. `app/io` publishes an
  immutable `Snapshot` of one generation's `db` / `redis` / `amqp` / `kafka` /
  `rocketmq` / `uuidint` handles through `atomic.Pointer`; `appio.Middleware()`
  pins that snapshot into the request context at entry, and
  `appio.WithContext(ctx)` resolves repositories, services and the rate limiter
  against the same generation even if a reload publishes a successor
  mid-request. Scripts run the same generation: the demo's `script.Register`
  wraps DB-backed scripts so `app/io` is started and stopped around every run,
  giving the shared service and repository code a complete generation; scripts
  declaring `WithoutDatabase` keep their own wiring. Tests still pin a
  connection with `appio.PublishDB`. Generation-bound lookups
  no longer go through package-level registries: `db`/`redis`/`amqp`/`kafka`/
  `rocketmq` `Set.NewIO` resolve that generation's `Registry`,
  `Snapshot.DBNewIO` / `RedisNewIO` / `AMQPNewIO` / `KafkaNewIO` /
  `RocketMQNewIO` expose the same lookup on a pinned snapshot, the uuidint
  generator is built with `support/uuidint.WithClient` from the generation's
  Redis client (and the script opens its own client), and the rate limiter
  resolves Redis via the pinned snapshot. The `app/io/*.NewIO` package
  functions remain for context-free callers and read the installed `Set`.
- Named-instance registries (`support/db`, `support/pg`, `support/sqlite`,
  `support/redis`, `support/amqp`, `support/kafka`, `support/rocketmq`):
  first-time initialization is per name and runs outside the registry lock.
  Different names open in parallel; concurrent first calls for one name share a
  single attempt and receive the same handle; a failed attempt is not cached,
  so the next call retries; a `Close` that starts while an open is in flight
  releases the late connection instead of publishing it, and the caller gets
  `ErrNotConfigured`. `Install` is ignored once `Close` has started. Package
  level `NewIO` / `CloseAllIO` stay as single-app compatibility entry points;
  `reg.NewIO` / `reg.Close` are the generation-isolation boundary.
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
- module mounting: several modules may return `Prefix() == ""` and mount at the
  root router. Prefix deduplication is skipped only for the empty normalized
  prefix, because the real route prefix then comes from the module itself
  (usually a contract route group); duplicate routes are still rejected at gin
  registration and surface as startup errors through the module panic
  recovery. Non-empty mount prefixes are still deduplicated.
- `examples/demo-service`: the product module now serves the contract's read-only
  product routes. `productroute.Products` mounts `/api/v1/product` with
  `GetProduct` (`/products/{id}`) and `ListProducts` (`/products`, paged); the
  controller became a `demoservice.ProductsService` adapter over
  `ProductService`, the service and repository gained paged listing, and the
  module root returns `Prefix() == ""` and binds the group. Product errors use
  the new 60000-60999 band (`ErrCodeProductNotFound = 60001`).
- Middleware order is an explicit contract. `buildHTTPWith` documents the
  assembly order (RequestID → Telemetry Trace → HeaderPropagation →
  SecurityHeaders → AccessLog → CORS → Recovery → BodyLimit → app middleware →
  probes → base router → registrars → module routes) and the reasons for each
  position. Tests fix the observable order, the recovered-panic path (Envelope,
  access-log 500, retained `req_id`) and the boundary that panics raised before
  `Recovery` are not caught. `make check` also runs `go vet` for the demo
  module now.
- `examples/demo-service`: group1 closes the produce → consume loop.
  `worker/group1/producer.go` is `order-event-producer`, a worker that every
  five seconds publishes the same `order.paid` event for order 1 to the four
  transports its sibling consumers subscribe to, so one `worker --group=group1`
  process demonstrates the full path without a second terminal. The contract
  lives with the consumers: topic, exchange, binding and tag are read from the
  subscription configs, and broker addresses from the same `ConnectionsFrom`
  table, so moving a subscription moves its producer leg. Each payload carries
  `reason` naming the queue it arrived through (`记录来自 kafka 队列`), and the
  four deliveries share one event id — safe because the idempotency ledger is
  keyed by `(consumer, message_id)`. Kafka publishes with the order id as key
  and the event id in the `message_id` header, so the consumer's dedup takes
  the first-choice path instead of falling back to `topic/partition@offset`;
  AMQP routes on the event type through the `order.*` binding, RocketMQ takes
  the subscription's first tag, and the Redis queue pushes the envelope as-is.
  Sinks open once in a loop and close in reverse; any transport that fails to
  open aborts the worker rather than fanning out three quarters of the demo.
- Delayed queues end to end. `support/queues.DelayQueue` already had `Add` /
  `Pop` / `Retry` / `Len` but no resident loop: it now has `Consume`, with the
  same signature as `Queue.Consume`, so the existing `queuesconsumer` worker
  adapter drives both unchanged. `ConsumeOptions.PopLimit` bounds one batch of
  due messages; a failed message is written back with a fresh due time up to
  `MaxRetries`. `support/consumer.NewDelayQueues` adds the matching
  subscription (`DelayQueuesConfig` / `DelayQueuesMessage`, `source`
  `delay_queues`) on top of that adapter.
  `support/rocketmq.PublishOptions` gains `Delay`, the relative form of the
  existing `DeliverAt`; setting both, or a negative delay, reports
  `ErrInvalidDelay`.
  `examples/demo-service/worker/group1` mirrors the immediate loop:
  `delay_producer.go` schedules one `order.paid` every 10s due 5s later into a
  Redis ZSET and a RocketMQ DELAY topic, and `delay_queues_order.go` /
  `rocketmq_delay_order.go` consume them, logging the observed wait. RocketMQ
  v5 rejects a delayed message on a NORMAL topic, so the sample documents the
  `-a +message.type=DELAY` topic creation. Verified against the local brokers:
  Redis waited ~6s (second-granularity enqueue stamp plus the 200ms poll) and
  RocketMQ scheduled 5s / waited ~5.1s, with one idempotency row per consumer
  per event.
- AMQP delayed queues complete the trio. `support/amqp` already had the TTL +
  dead-letter workflow (`DelayConfig`), but only a queue-level fixed TTL and no
  resident subscription. `PublishOptions.Delay` is now the relative form of
  `Expiration`: it becomes the per-message TTL whose expiry dead-letters the
  message into `DelayConfig.Target`, and `DelayConfig.TTL` doubles as its upper
  bound because the broker applies the smaller of the two. The transport
  rejects a delay below 1ms, one past the queue TTL (otherwise silently
  clamped), one combined with `Expiration`, and one on a Queue with no
  dead-letter target (where the message would simply vanish at expiry).
  `support/consumer.NewDelayAMQP` adds `DelayAMQPConfig` / `DelayAMQPMessage`
  with `source` `amqp_delay`. It declares both halves of the topology and reads
  the target through the existing `amqpconsumer` adapter, whose `consumeQueue`
  already switches to `Delay.Target.Queue`. `QueueConfig()` is the one topology
  both sides publish through, so their queue arguments cannot disagree and trip
  PRECONDITION_FAILED. The demo's `delay_producer.go` gains a third sink and
  `delay_amqp_direct_order.go` the matching consumer. RabbitMQ only checks the head of
  a queue for expiry, so the docs call for one delay per staging queue.
  Verified against the local broker: four events, each arriving on all three
  delayed transports at ~5.3-5.8s, one idempotency row per consumer per event.
- AMQP delayed queues now work with topic exchanges. c9aba06 always declared
  `x-dead-letter-routing-key` with the target's fixed key; on a topic target
  that overwrites every message's routing key, so `order.paid` and
  `order.cancelled` become indistinguishable and a literal `order.*` can miss a
  concrete binding altogether, dropping the message.
  `DelayConfig.PreserveRoutingKey` omits the argument so RabbitMQ dead-letters
  with the original key, and removes a hand-written value from
  `Topology.Arguments` too — the mere presence of the argument overrides the
  key. The staging arguments now come from a pure function, so the tests cover
  both paths without a broker.
  `support/consumer.DelayAMQPConfig` forwards the flag through `QueueConfig()`
  and rejects topic topologies with no key, since an empty topic binding matches
  nothing and leaves the queue permanently idle. The demo adds
  `delay_amqp_topic_order.go` and a fourth sink: both halves are topic exchanges
  bound to `order.*`, the producer supplies `order.paid` as the concrete routing
  key, and the handler logs the key the broker forwarded.
  Verified on the local broker: four events arrived on all four delayed streams
  at ~5-6s, the topic leg logged `routing_key=order.paid`, and `rabbitmqctl`
  showed `x-dead-letter-routing-key` present on the direct staging queue but
  absent on the topic one.
- AMQP delayed queues now take the delay only at publish time.
  `DelayConfig.TTL` is optional: zero (the default) declares no
  `x-message-ttl`, so the producer's `PublishOptions.Delay` is the sole source
  of the wait and becomes mandatory — `Publish` answers `ErrDelayRequired`
  rather than letting an undated message sit in the staging queue forever. A
  positive TTL still gives the classic fixed-delay queue, acting as the default
  delay and the upper bound, so existing subscriptions keep working.
  This removes a second way to set the delay from the template, matching the
  RocketMQ and Redis sinks, and fixes two traps of the queue TTL: it silently
  clamped a larger per-message delay, and a forgotten `Delay` made a message
  quietly arrive late instead of failing at the call site. `delaySourceArgs`
  now omits `x-message-ttl` (and deletes a hand-written one),
  `messageExpiration` only enforces the upper bound when a queue TTL exists,
  and both validation paths accept zero.
  The demo's two AMQP delayed subscriptions drop `TTL` and rename their staging
  queues to `*.staging`: the old names implied a fixed 5s delay, and a queue
  already declared with `x-message-ttl` cannot be redeclared without it
  (`PRECONDITION_FAILED`). Verified on the local broker: three events arrived on
  all four delayed streams at ~5-6s, the topic leg still logged
  `routing_key=order.paid`, and `rabbitmqctl` showed no `x-message-ttl` on the
  new staging queues.
- `support/consumer.DelayAMQPConfig` gains `Passive`, forwarded to both halves
  of `QueueConfig()`: the staging topology and `Delay.Target`. Use it when
  another process — often a Java service — owns the declarations. `Publish`
  always goes through `channel() → declare()`, and a normal `QueueDeclare`
  compares every argument, so a foreign staging queue carrying
  `x-message-ttl=60000` answered `PRECONDITION_FAILED` before a message was
  even sent. Passive declarations only check that the names exist. Names, keys
  and `TTL` still drive publishing, binding and the pre-publish delay checks,
  so they must match the other side; the broker dead-letters with the arguments
  on its own queue, not with the `DelayConfig` built here. Verified against a
  foreign topology with a 60s queue TTL: the default declaration failed with
  406, while `Passive` published a 10s delay that arrived at ~10s; setting
  `TTL: 60s` additionally rejected a 90s delay with `ErrInvalidDelay` instead
  of letting the broker clamp it.
  Added a cross-language section to `support/amqp/README.md` and notes to the
  consumer and demo worker READMEs.
- `examples/demo-service`: a Kafka manual-commit sample. `orderKafka` already
  ran with `CommitMode: manual`, but its handler said nothing about what that
  means, so a reader could not tell a commit from an acknowledgement.
  `kafka_manual_order.go` adds `order-kafka-manual-consumer` on its own topic
  and consumer group, with a handler that logs the partition and offset and the
  decision the shared chain will make, then returns the error unchanged.
  The point it teaches is that "manual" does not mean the handler calls
  `CommitRecords`: the transport disables franz-go auto commit and commits the
  last accepted record of a partition's batch, so `nil` commits, a retryable
  error holds the offset and redelivers, and a `Permanent` error parks the
  message but still commits so a poison record cannot stall the partition.
  `producer.go` gains a fifth sink for that topic, with both Kafka sinks sharing
  one publisher built from their own subscription config.
  Verified on the local broker: six normal records left CURRENT-OFFSET 6 / LAG
  0; an event for a missing order was redelivered at the same offset three
  times before `MaxAttempts` parked it as dead and the partition moved on; an
  unknown event type was discarded yet still committed. The ledger ended with
  two dead rows (attempts exhausted / unsupported type) and every later record
  done. The README now documents all three paths, the consumer-group query and
  the need to create the topic first — the local broker has auto-create off, and
  a missing topic only surfaces as `UNKNOWN_TOPIC_OR_PARTITION` at publish time.

### Changed

- `examples/demo-service`: `uuidint.redis_instance_name` and `uuidint.key` are
  required when `uuidint.enabled` is true. Their `default` tags and the matching
  fallbacks in `withDefaults()` are gone, so omitting either fails at config
  load with `config: uuidint: ... is required` instead of silently allocating
  IDs in the `ratelimit` instance under `demo:uuid`. Numeric settings (TTL,
  retries, pool interval) keep their defaults. `Open` repeats the check because
  a script can modify `Config` after `config.Load` validated it.
- `examples/demo-service`: `appio.WithContext` now panics when neither the
  context nor the published generation carries a snapshot, instead of returning
  a nil handle that panicked later inside GORM. Every entry point (request
  middleware, script `withIO`, io module `Start`) publishes before shared code
  runs, so a missing generation is a wiring bug and should surface at the first
  lookup. A new `shell/check-io-access.sh` guard, wired into `make guards` in
  both the demo and the root Makefile, rejects package-level handle accessors
  (`appdb.Default()`, `appredis.Cache()`, ...) and self-published snapshots in
  `service` and `repository` packages, keeping `appio.WithContext(ctx)` the only
  lookup path for code shared by requests, scripts and daemons.
- `examples/demo-service`: the `nacos-watch` module now runs its watcher and
  the reload executor on separate goroutines. `Stop` cancels the watcher and
  waits for it to exit before the generation's handles are released, which the
  single-goroutine form could not do without deadlocking the reload that calls
  `Stop` from inside the change handler. Notifications are coalesced, so a
  change arriving during a reload triggers one follow-up pass against the file
  already on disk; reload errors are logged by the module instead of being
  returned to `nacos.Watch`, which only logged them anyway.
- Go comments in this repository, `examples/demo-service` and
  `gofy-interface-service` are now bilingual: every existing comment block
  keeps its English text and gains a Chinese section after it (pure-Chinese
  blocks gained English above them). The convention is recorded in `AGENTS.md`
  in both repositories; code, tags, directives and generated artifacts are
  unchanged.
- `gofy-interface-service` now declares
  `module github.com/bingcool/gofy-interface-service`, matching the repository,
  and every import in that repository and in `examples/demo-service` was
  updated. It still requires `github.com/bingcool/gofy v0.0.0` with a local
  `replace` because neither repository has a release tag yet; removing the
  replace, pinning a published version and dropping the sibling-directory
  assumption remain release steps.
- `support/otel`: a provider no longer touches process-wide OpenTelemetry state
  unless `Config.Global` is true. Export failures are reported through the
  provider's logger with the existing one-minute dedupe, so the default
  `Global=false` can coexist with other instrumentation in the same process.
  `Global=true` installs the error handler, tracer provider and propagator
  together; `Shutdown` never restores or closes the process-wide values, and
  only one App per process should enable it.
- `support/db`, `support/pg`, `support/sqlite`: SQL logs keep the statement
  template instead of expanded arguments. The custom GORM logger now implements
  `gorm.ParamsFilter` and returns a nil var list by default, so slow queries and
  failed queries log placeholders (`?` / `$n`) plus `elapsed`, `rows` and
  `error`/`threshold`, but not phone numbers, emails, tokens or other values.
  The dead `parameterized` field is replaced by `Config.LogParameterValues`
  (default `false`); setting it to `true` restores expansion for local
  debugging only and must not be used in production.
- `support/kafka`: consumer offset commits are now manual by default. The new
  `ConsumerConfig.CommitMode` (`CommitModeManual` / `CommitModeAuto`) states the
  delivery contract directly: manual (the zero value) commits only records the
  handler accepted, so a failed record is redelivered; auto lets franz-go
  commit polled offsets on `AutoCommitInterval` and can drop a failed record, so
  it must be requested explicitly. `DisableAutoCommit` is deprecated: `true`
  still means manual, but `false` no longer enables auto commits — set
  `commit_mode: auto` to keep the old behavior. Unknown modes fail in
  `NewConsumer` with `ErrInvalidCommitMode`.
- Package-level named-instance helpers (`Configure`, `NewIO`, `CloseAllIO` in
  `support/db`, `pg`, `sqlite`, `redis`, `amqp`, `kafka`, `rocketmq`) and
  `support/uuidint.WithInstance` are now documented as single-app compatibility
  entry points: they follow the last installed `Registry` in the process and do
  not promise isolation between several apps or reload generations. No API was
  removed; generation-aware code should use `reg.NewIO` / `reg.Close` and
  `uuidint.WithClient`. These entry points are candidates for deprecation in a
  future release, not in this one.
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
  (`RateLimit.RedisAlias`, defaulting to `ratelimit`) through the request-pinned
  `app/io` snapshot (`appio.WithContext(ctx).RedisNewIO`) and wraps the client
  in `redisstore`, so several instances share one set
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
- `README.md` was brought back in sync with the code: the middleware order
  contract, the generation lease and drain steps in `Reload` (including
  `ReloadSpec.MiddlewareRegistrars`), the four log streams and
  `access_outputs`, placeholder-based SQL logging, Kafka `CommitMode`, the
  `global` opt-in for OTel, `make check` as the only gate (the repository has
  no GitHub Actions), and the `storage/log/{http,access,script}` demo layout.

- `worker --group='*'` runs every registered group in one process, for local
  runs and smoke tests: the pid file becomes `-worker-all.pid`,
  `stop --worker='*'` resolves that same file, and the first group to fail
  cancels the rest. `--name` is rejected in this mode because a worker name
  belongs to exactly one group, and the process-wide pid/probe/stop granularity
  is unchanged, so a deployment still runs one group per process.

- `support/worker.Ticker(ctx, intervalMS, fn)` wraps the resident polling loop: a
  failed call is logged at warn and the next tick still runs, a panic is
  recovered with a stack, and cancellation returns nil so the runner records a
  clean stop. The interval is a millisecond count rather than a duration, which
  keeps "every 5000 ms" readable at the call site; zero and a nil callback are
  rejected up front, so a bad value fails at startup instead of spinning. The
  demo's scan, heartbeat and summary members now use it instead of their own
  ticker/select blocks.

### Fixed

- `support/rocketmq`: the v5 SDK no longer tries to create `/logs` on macOS.
  `rocketmq-clients/golang/v5@v5.1.2` builds its lumberjack log root as
  `os.Getenv("user.home") + "/logs/rocketmqlogs"`. `user.home` is a Java system
  property, not a Go environment variable, so in a Go process it is empty and
  the default becomes `/logs/rocketmqlogs/...`. The SDK's `init()` builds that
  writer before `main`, and its first log line makes lumberjack call
  `MkdirAll("/logs")` — the macOS root volume is read-only, so every SDK log
  line instead emitted `write error: can't make directories for new logfile:
  mkdir /logs: read-only file system` on stderr and the diagnostics were lost.
  `support/rocketmq/sdklog.go` now sets `rocketmq.client.logRoot` to a writable
  temp directory and rebuilds the SDK logger on the first `New` / `NewConsumer`
  (lazily via `sync.Once`, since public packages may not use `init()`), before
  anything is written; an explicit value of that variable always wins.
  Verified with
  `worker --group='*'`: zero `/logs` errors and
  `rocketmq_client_go.log` created under the temp root. Upstream v5.1.4 fixes
  the default with `os.UserHomeDir()`, but that release also renames the
  `WithSimpleAwaitDuration` / `WithSimpleSubscriptionExpressions` options and
  changes producer startup behavior (a registry test with a real client hung),
  so this pins the equivalent fix on the version we use.
- `logger`: a colored error block now ends with a newline. It used to append
  `\x1b[0m` after the last content newline without terminating its own line, so
  the next entry — or zap's own `write error` on stderr — was glued onto the
  reset code. Redirected stdout then held lines like
  `\x1b[0m{"level":"info",...}`, which a JSON collector cannot parse and a
  `grep` reports as a missing line. The reset code now sits at the end of the
  last content line and the block ends in `\n`.
  `TestColorAlwaysDoesNotGlueEntriesTogether` pins the boundary, and
  `TestConcurrentSinksKeepEveryEntry` verifies 4x200 concurrent entries reach
  both a console and a file sink in all three color modes: the tee itself never
  drops lines. (An earlier note blamed `color: always` for a "stdout drops
  lines" observation; that was a misdiagnosis — a second worker process was
  appending to the same rotated log, so the file was the union of two processes
  while the redirected stdout belonged to one. The block termination above was
  the only real defect.)
- `Execute` prints an error to stderr whenever no logger exists yet, not just
  for usage errors: a mistyped `worker --group` used to exit 1 with no message
  because the group is looked up before the logger is built and `App.Logger()`
  in main was still a Nop. Usage errors keep reporting the corrected form
  (`--order-id=1222`), and script `Run` errors reach `App.Logger()` because the
  runtime logger is now exposed on the app.
- `support/worker`: the drain context no longer expires before shutdown. It was
  built as `WithTimeout(WithoutCancel(ctx), DrainTimeout)` when `Runner.Run`
  started, so any worker running longer than the budget — 20s by default — handed
  every in-flight handler a context that had already expired, and consumers
  failed each message with `context.DeadlineExceeded`. The budget now starts when
  the run context is cancelled; `TestDrainContextDoesNotExpireBeforeShutdown`
  pins it.
- `support/rocketmq`: an empty long poll is no longer reported as a receive
  error. Only the client-side deadline (`CODE=DEADLINE_EXCEEDED`) was treated as
  idle; when the v5 proxy's long-poll window closes first it answers gRPC 40401
  `MESSAGE_NOT_FOUND` / "no new message", which fell through to the error path —
  so an idle topic logged `rocketmq consume error` once per `AwaitDuration`, per
  worker, and every one of those polls also paid the 1s receive backoff.
  `isIdleReceive` now recognizes both shapes, through the SDK's `ErrRpcStatus`
  (which survives `%w` wrapping) as well as the deadline string, and real
  failures — proxy down, an RPC status that is not 40401 — still report.

### Changed

- Split the gofy-specific adapter into a new `gofy-adapter` module.
  `gofy-interface-api` is now framework independent: its go.mod only requires
  kin-openapi, swgui and yaml.v3, with no replace and no gofy/gin dependency.
  `adapter/gofy` and `client/nacos` moved to the new module; demo-service gained
  one require and one replace; the import guard and refcheck rules were updated.
  See docs/interface-api-split-design.md §12.

- Document why `examples/demo-service/go.mod` must keep its own
  `gofy-interface-api` replace: Go only honours replace directives in the main
  module, and `v0.0.0` is an unpublished placeholder. §5.3 / §6.2 / §11.8
  record the three experiments and defer removal to the publish step (T2-2).

### Changed

- `support/internal/instanceinit`: a hit in a named-instance Registry is now
  lock-free. Every successful open republishes an immutable snapshot of the
  value map, and `Get` reads it with a single atomic load before touching the
  mutex, so resolving a handle on every query costs an atomic read instead of a
  lock round trip. Only the first open and `Close` take the mutex; `Close` marks
  the slots down first so a lookup never waits behind the drain.
- `examples/demo-service`: the IO snapshot has one way to resolve a handle —
  `DBIO`, `RedisIO`, `AMQPIO`, `KafkaIO`, `RocketMQIO`, all returning
  `(handle, error)`. `DB`, `DBInstance`, `Redis`, `AMQP`, `Kafka` and
  `RocketMQ` are gone, and `DBIO("default")` resolves the framework-managed
  default, so no caller has to know which pool lives outside the registry.
  `UUID` stays: a generator is not a connection and has no Registry to resolve.

### Removed

- The legacy `gofy-interface-service` contract repository has been deleted by
  its owner. No `go.mod`, build script or Makefile target references it, and all
  four gates still pass after removal. The import mapping in
  `docs/interface-api-split-design.md` §10 is kept for historical links; the
  legacy design documents remain as historical records.

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
