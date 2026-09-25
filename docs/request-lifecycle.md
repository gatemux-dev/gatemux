# Request lifecycle controls

For process termination, authenticated drain, worker joining and external stop
budgets, see [shutdown and recovery](shutdown-recovery.md).

Implemented 2026-09-06. These controls bound resource lifetime; they do not imply
that every provider modality has complete pricing, guardrails, or SDK parity.

```yaml
server:
  v1_deadline: 90s
  streaming:
    first_event_timeout: 30s
    idle_timeout: 30s
    write_timeout: 15s
    keepalive_interval: 0s
deployments:
  - name: reasoning-provider
    type: openai
    upstream_model: your-model
    api_key_env: PROVIDER_KEY
    streaming:
      first_event_timeout: 60s
      idle_timeout: 45s
      write_timeout: 10s
      keepalive_interval: 15s
```

The outer `v1_deadline` always wins, including connection, upload, fallback, and
response forwarding. Socket read deadlines interrupt stalled inbound uploads on
cancellation. Configure total/read timeouts for the largest permitted upload.

- Chat and native Messages: the first deadline includes connection/headers and
  the first complete non-empty data event. Partial bytes, SSE comments, and native
  Messages `ping` events are not progress. Idle resets only on progress.
- Native Messages ends at `message_stop`, not `[DONE]`; a missing terminal event
  is an interrupted response. Event names and unknown JSON fields are preserved.
  `anthropic-version`/`anthropic-beta` are forwarded; upstream credentials replace
  gateway credentials. See the [official streaming contract](https://platform.claude.com/docs/en/build-with-claude/streaming)
  and [API header contract](https://platform.claude.com/docs/en/api/overview).
- Opaque SSE passthrough allows clean EOF, since it has no universal terminal
  event. Binary/JSON passthrough has first-byte/idle limits and a 32 KiB copy buffer.
- Writes and flushes have an independent timeout; upstream idle accounting pauses
  during data writes, preserving its remaining time. Client cancellation closes
  upstream bodies and releases admission permits.
- Optional SSE comment keepalives start only after the first event. They do not
  reset first-event, idle, or total deadlines. Enabled streams add exactly one
  joined, demand-driven reader goroutine with no unbounded event queue. Disabled
  streams retain the synchronous reader. Keepalive writes have the write limit.
- HTTP error diagnostics are limited to 64 KiB (plus a truncation marker for
  non-SDK adapters). Header bytes, dialing, TLS, connection counts and idle pools
  are bounded. Credential-bearing redirects are not followed. Native/opaque
  forwarding strips hop-by-hop and Connection-nominated headers, and upstream
  cookies cannot modify the gateway admin origin.
- Incomplete responses after HTTP headers are committed abort the downstream
  transport. Usage/audit records can therefore show 499/502/504 despite the
  original HTTP 200. No upstream retry occurs after visible data.

## Deployment overrides

Models → Deployments → Add/Edit → Response lifecycle. Admin `streaming` is a JSON
object with duration strings matching YAML. `null` restores all gateway defaults;
omitting it on PATCH preserves the policy. Within an override, omitted/zero
timeouts inherit; omitted/zero keepalive disables comments for that deployment.
Negative durations, values over 24h, unknown fields, numeric JSON durations, and
nonzero keepalive intervals below 10ms are rejected. Registry refresh applies new
settings to new attempts, not already-running streams. YAML deployment sync is
authoritative on restart.

## Upload memory and disk

Transcription/translation request envelopes are capped at 32 MiB and individual
files at 25 MiB; ordinary data-plane bodies remain capped at 8 MiB. Multipart
file data above 1 MiB spills to temporary disk. A single pipe producer re-encodes
the model alias and streams file bytes without a second in-memory payload. It
is closed/joined before temporary files are removed, including early rejection,
disconnect, and panic. Non-file fields retain Go's bounded multipart overhead.
Operators must provision and quota temporary disk for their configured global
admission cap; 1024 simultaneous maximum-sized uploads are not a small workload.

## Budget cleanup

Budget admission, pricing lookup, settlement, and optional payload persistence
each have bounded database IO (2s per phase). New requests commit a pre-call
journal; usage and reservation settlement commit atomically. Panics preserve the
original panic while the request finalizer records conservative cost evidence.
Failed completion remains recoverable by a bounded, replica-safe worker after
the durable deadline. See [durable accounting](durable-accounting.md) for exact
states, health pauses, migration limits and process-interruption tests.

Remaining qualification: live paid-provider/cloud credential refresh and SDK
transport behaviors are not certified by local mocks. Opaque modality cost
accounting is separate; zero-cost audit entries do not mean the provider is free.
