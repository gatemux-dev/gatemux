# Shutdown and recovery

Pilot Beta implementation, 2026-09-16. These are local fault-test guarantees,
not zero-downtime Kubernetes qualification. The later 30-minute local soak
passes; its evidence and exclusions are recorded below.

```yaml
server:
  shutdown:
    grace_period: 30s
    cleanup_timeout: 10s
```

Omitted or zero durations inherit these defaults. Negative durations are
invalid; grace is capped at 24h and cleanup at 5m. The application can take
grace plus cleanup to exit. Compose now allows 45s; the Helm default is
`terminationGracePeriodSeconds: 45`. Increase the external stop allowance when
increasing either duration, and include any external pre-stop/routing delay.
Do not put master keys into Kubernetes HTTP hook definitions or ConfigMaps.

## Drain sequence

1. An administrator may `POST /health/drain` with the master key or an admin
   session. Managers and unauthenticated callers cannot drain the gateway. The
   endpoint returns `{"draining":true}`; it does **not** stop the process.
2. Drain is irreversible until restart. `/readyz` returns 503; `/healthz` stays
   healthy. New `/v1/*` (including models/stored-response reads), `/passthrough/*`
   and admin inference replays get `503 server_draining` and `Retry-After: 1`.
   Requests already in the global queue recheck drain before authentication and
   provider IO. Requests that already passed that check may finish normally.
3. Remove this instance from routing and allow the load balancer to observe the
   change, then send SIGTERM. SIGTERM also starts drain automatically before
   closing listeners, but cannot guarantee routing propagation before closure.
4. Active inference gets the configured grace window (or its earlier existing
   request deadline). Grace expiry cancels upstream requests, streams and uploads
   and closes downstream sockets. A separate bounded cleanup phase waits for
   request finalization, joins workers, closes owned Redis clients and flushes
   telemetry. Grace expiry or incomplete cleanup returns a shutdown error, not a
   false clean exit. Calling shutdown repeatedly observes the same result.

The process-local `gatemux_draining` metric changes from 0 to 1 on the private
metrics listener. Admission/queue/deployment gauges and durable accounting remain
the other recovery signals. Readiness dependency checks share a 2s context.
Redis socket/dial/pool waits are capped at 100ms for rate policy, 100ms for
distributed concurrency, 50ms for breakers, and 250ms for prompt cache; automatic
Redis retries are disabled for these clients. Configured policy fails closed;
cache and breaker callers retain their documented fallback behavior.

## Worker ownership and callbacks

Each constructed server owns one JWT refresh, alert evaluator, payload retention,
provider health sampler, stored-response retention, concurrency-policy watcher
and accounting recovery worker. They accept cancellation and expose completion
signals. Listeners cannot start twice or restart a drained instance. Binding
either listener fails startup; command exit also cleans up the constructed server.
Metrics registries are per instance; this does not claim independent global OTEL
tracer providers for multiple gateways embedded in one process.

Callback registration closes at startup and is capped at 32 sinks. Each sink has
one worker and its existing 10,000-event queue, plus one bus completion worker.
Cancellation stops sends, counts queued/late events as dropped, joins workers and
flushes the archive's final buffered gzip chunk with an independent 2s context.
Callbacks remain **best effort**: queued events are not durably drained or replayed,
archive upload/flush failure is logged, and delivery counters do not prove remote
durability. The `s3` configuration still uses local archive files unless an uploader
is supplied programmatically. An arbitrary custom sink or stalled filesystem that
ignores cancellation can exceed its own deadline; gateway cleanup reports timeout
and does not wait forever. This is not lossless callback delivery.

JWT/JWKS no longer creates detached per-issuer library refresh loops. The joined
5-minute verifier refresh also refreshes issuer keys, with a total 10s cycle
budget. Issuer fetches have a 2s deadline, 1 MiB body limit, bounded connection
pool, and no redirects. Unknown signing-key refresh runs in the inference request
context, with one refresh per issuer per five minutes per cache generation and a
1ms rate-limit wait. Cached keys remain during transient issuer failures; removed
issuer assignments disappear on successful team-list refresh. This is lifecycle
hardening of the existing JWT surface, not full identity/security qualification.

## Evidence and remaining gates

`internal/server/lifecycle_test.go` covers real HTTP SSE completion/forced cancel,
finalizer joining, queued rejection, admission/drain races, repeated shutdown,
listener bind failure and concurrent startup/shutdown. Private-schema integration
tests additionally verify authenticated drain, repeated instance/worker ownership,
metrics isolation, bounded Postgres pool starvation and recovery, Redis blackhole
timeouts, and real guarded/budgeted streams with four distributed scopes. All
active leases are checked before drain and zeroed afterward; usage/reservations
settle once. Existing crash-process accounting and Redis contention/expiry suites
remain part of recovery evidence. JWT and callback tests cover cancellation,
rotation, rate-limited misses, bounded bodies, queue drops and final archive flush.

Incomplete provider evidence remains estimated/unknown, not silently refunded;
see [durable accounting](durable-accounting.md). Process death can leave an intent
until its recorded request deadline plus 30s. A live replica then recovers bounded
batches without replaying inference. Cleanup timeout does not erase that intent.

The configured-policy 30-minute local soak passed on 2026-09-19 from clean
source `a764bbd`. Private local test records show zero unexpected errors/drops,
stable sampled memory/queues, complete
accounting and post-disconnect/drain/restart recovery. The earlier failed run
is retained; budget admission's historical-read bottleneck was corrected first.

Still required: multi-host/Redis Cluster and Kubernetes rolling-upgrade
qualification, provider-cloud validation and reproducible public load evidence.
Do not infer those from the local configured-policy pass.
