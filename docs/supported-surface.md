# Supported Surface

Status: Active
Last updated: 2026-09-24

This document defines what GateMux currently considers stable, beta, and experimental for open-source users.

The overall product is a public alpha. These tiers describe feature maturity,
not general availability or a production-scale qualification. See
[alpha limitations](public-alpha.md).

## Stability Levels

- `Stable`: expected to work, documented, and part of the public support contract
- `Beta`: usable, but still likely to change in behavior or API shape
- `Experimental`: present in the repo, but not yet part of the compatibility promise

## Stable

### Inference API

- `POST /v1/chat/completions`
- `POST /v1/embeddings`
- `GET /v1/models`
- SSE streaming for chat completions
- Global per-request deadline applied at the edge (`server.v1_deadline`)
- Lossless unknown-field forwarding for OpenAI-compatible chat and embeddings paths
- Token-array embedding input for compatible deployments; translating Gemini/Cohere adapters reject IDs safely. See [token accounting](token-pricing.md).

### Control Plane

- teams
- virtual keys
- key expiry and rotation
- key and team allowlists
- user-scoped keys
- service accounts (separate identity for non-human automation)
- deployments
- aliases
- pricing
- optional cache read, cache write/TTL and reasoning price overrides with inclusive-token accounting, whole-cent rounding and conservative reservation estimates
- spend reporting
- audit history
- provider health (point-in-time + history sampling)
- role-based admin UI
- grouped admin workspaces with overview, scoped team budget/privacy sections, URL-backed team/model navigation, and mobile drawer
- onboarding setup wizard

### Reliability And Cost Controls

- Redis-backed key and team RPM / TPM enforcement
- team and user budgets
- retry-before-first-byte
- ordered fallback
- per-attempt timeout
- global per-request deadline
- distributed circuit breaker (Redis-backed, with local fallback on outage)
- bounded per-process data-plane admission with separately capped active and queued requests
- per-deployment local concurrency caps with saturation-aware fallback and full-stream accounting
- routed chat first-event (30s), stream-idle (30s), and downstream-write (15s) deadlines via `server.streaming`; bounded 1 MiB SSE events; fallback only before the first data event is committed
- native Messages/opaque SSE/binary passthrough response deadlines, bounded multipart uploads, per-deployment streaming policies and opt-in keepalives; see [request lifecycle](request-lifecycle.md) for exact limits and remaining modality accounting gaps
- 64 KiB upstream diagnostics and conservative panic-safe budget settlement with bounded database cleanup IO
- Redis-backed distributed team/key/user/service-account concurrency caps with atomic multi-scope admission, fail-closed coordination, and crash-expiring full-request leases

### Identity And Auth

- email/password sign-in with bcrypt
- HttpOnly password/invite session cookies (SameSite=Strict); OIDC uses Lax
- Configurable Secure cookies behind HTTPS proxies; cookie-only password login
- Server-side master-key disable policy after administrator provisioning
- session management (list / revoke individual sessions, revoke-all on password change or disable)
- password change (self-service) and admin-issued password reset
- account disable (revokes all sessions)
- OIDC sign-in with auto-provisioning and claim-based role/team mapping
- 3-tier RBAC (`admin` / `manager` / `member`)
- master-key bootstrap (gated behind an "Advanced" affordance)
- security headers, body-size limit, and bounded atomic login/reset throttling
  shared through configured Redis; exact policy in [deployment](deploy.md)
- trusted-proxy CIDR config so `X-Forwarded-For` can be honored without spoof risk

### Provider Support

- OpenAI
- Anthropic
- Azure OpenAI
- OpenAI-compatible endpoints
- Ollama via OpenAI-compatible path
- vLLM via OpenAI-compatible path

### Deployment

- Multi-stage Dockerfile with embedded UI
- Docker Compose stack (Postgres + Redis + GateMux + optional dex)
- Helm chart (`deploy/helm/gatemux`) with config checksum re-roll, autoscaling, and read-only root filesystem
- Migrations apply on container boot

## Beta

### Virtual-key budget controls

- Budget-at-issue for team/user and service-account keys, validated general edits,
  owning-team budget summary/partial cap updates and admin UI workflows. Uses
  the existing UTC team window and reservation ledger, including earlier usage.
  Positive key caps now fail closed on unmetered routes and unsupported charge
  dimensions; chat forwards a default output ceiling when omitted. Null/zero
  mean no key-specific cap. Rotation is per-key-ID, not a shared rotation-family
  budget. Exact API, compatibility changes and tests: [key budgets](key-budgets.md).
- Reversible key pause and bounded rotation grace periods. Paused keys cannot
  authenticate or rotate, including pause-before-rotation-lock interleavings.
  Migration 0040 and exact lifecycle semantics are documented in [key budgets](key-budgets.md).

### Manual price imports

- Operator-supplied GateMux JSON price lists with explicit provider identity,
  validated numeric rates and a review-before-write preview. No automatic price
  feed or atomic bulk update; see [token pricing](token-pricing.md).

### Shutdown and Recovery

- Administrator-only irreversible `POST /health/drain`, fail-fast readiness,
  bounded grace/cleanup phases, active-stream cancellation/finalization, joined
  background workers and callback archive flush. Configured-policy local fault
  tests and the 30-minute local configured-policy soak pass; Kubernetes/multi-host
  rollout qualification remains open. [Shutdown and recovery](shutdown-recovery.md) defines exact limits,
  callback drops, JWT refresh behavior and container stop budgets.

### Durable Accounting

- Pre-call metadata journal, atomic idempotent usage/settlement, bounded
  replica-safe interruption recovery and fail-closed completion health.
  Explicit cost evidence in Requests, spend caveats and CSV; missing token usage
  is not free inference. [Durable accounting](durable-accounting.md) defines
  upgrade/history, retention, pricing and performance-qualification limits.
- Transactional daily budget totals avoid per-request history scans for team,
  user, service-account, key and customer admission. Migration 0039 needs a
  drained single-owner backfill. The local 66-RPS 30-minute pilot soak passes;
  capacity, multi-host and production-upgrade qualification remain open.
  [Bounded budget reads](budget-daily-totals.md) documents exact semantics.

### Team Manager Workflows

- Team-scoped paginated/searchable member directory and model-name catalog,
  manager team/invite pagination, virtual-key and customer workflows, team spend,
  and administrator-only action separation. Qualified using real password
  sessions and a disposable database, not intercepted UI fixtures. Exact API,
  role boundaries and UI bounds: [manager workflows](manager-workflows.md).

### API Reference and Responses

- `/openapi/v1.json`, `/openapi.json` and offline `/docs`: route-derived inventory,
  explicit core inference schemas and representative official Python/Node SDK
  tests. Generic control-plane schemas are not a complete field contract.
- Native `/v1/responses` create/stream, owned get/delete/input-items and multi-turn
  on Responses-capable OpenAI-compatible deployments. Metadata-only principal/
  team bindings, 30-day retention, pinned-state routing and shared admission/cost
  controls. Explicit unsupported operations and limits: [API compatibility](api-compatibility.md).

### User Self-Service

- `/me/keys`
- `/me/usage`
- `/me/budget`

### Reporting

- customer registration modes, atomic budget/RPM/TPM admission, attributed spend and Team → Customers policy UI; see [customer controls](customer-controls.md) for estimated-accounting and trusted-identity limits
- spend timeseries
- usage aggregate views
- spend forecasts (per-team projection)
- CSV export endpoints

### Deployment And Routing Controls

- distributed model-alias/provider-type concurrency policy and Models → Concurrency UI
- deployment capability flags (chat / stream / embed / moderate / rerank / images / STT / TTS / messages-passthrough)
- alias cache settings
- alias routing strategy controls (priority, tagged, region, cost, latency)
- deployment tags
- request-payload retention (opt-in per team) with configurable retention window

### Guardrails

- Administrator-managed, team/model-assigned literal-term policies with request,
  response and bounded streaming block/redact/flag enforcement. Fail-closed
  validation/audit and explicit unsupported-surface denials. Protected traffic is
  a narrower plain-text compatible-chat contract, not PII/moderation coverage.
  Exact bounds, API, provider restrictions and tests: [guardrails](guardrails.md).

### Operational

- alert rules (budget threshold and budget-exceeded triggers)
- alert event history
- audit log search
- bounded guardrail catalog and scoped recent decision counters in the Guardrails workspace
- generic passthrough endpoints (`/passthrough/{name}/*`) — operator-configured proxies for arbitrary upstream APIs (OpenAI Files/Threads/Assistants, Langfuse, etc.) behind GateMux's auth, rate limits, and audit log; budget metering does NOT apply (no canonical token count for arbitrary endpoints)

## Experimental

These surfaces exist in the codebase but are not yet part of the stable open-source contract.

### Inference Endpoints

- `POST /v1/messages`
- `POST /v1/moderations`
- `POST /v1/rerank`
- `POST /v1/images/generations`
- `POST /v1/audio/transcriptions`
- `POST /v1/audio/translations`
- `POST /v1/audio/speech`

### Product Extensions

- customer concurrency release qualification (configured-cap soak, Redis Cluster and rolling upgrades remain)
- prompt caching
- callbacks and callback sinks (webhook, Slack, S3, Langfuse)
- passthrough helpers
- alert triggers beyond budget (error rate, provider unavailable)
- approval workflow for destructive admin actions
- request replay / correlation view

### Provider Packages

- Gemini
- Bedrock
- Cohere
- Fireworks
- Groq
- Mistral
- OpenRouter
- Together
- Vertex

## Support Policy

For now, the open-source support promise is:

- maintain the stable core
- accept feedback and fixes for beta surfaces
- treat experimental surfaces as subject to breaking change without the same compatibility guarantee

If a feature is not listed as `Stable`, do not build a production integration that assumes its current behavior will remain unchanged.
