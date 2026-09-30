<!--
Sync Impact Report
Version: (template) → 1.0.0
Added: all principles and sections (initial ratification).
Sources: CONTRIBUTING.md, SECURITY.md, .github/pull_request_template.md,
docs/public-alpha.md, docs/supported-surface.md, docs/deploy.md,
docs/request-lifecycle.md, docs/durable-accounting.md, docs/key-budgets.md,
docs/api-compatibility.md, docs/shutdown-recovery.md, docs/rename-upgrade.md.
Templates: plan-template.md "Constitution Check" resolves against the gates below;
spec/tasks templates need no changes.
-->

# GateMux Constitution

GateMux is an open-source LLM gateway: one OpenAI-compatible API in front of many
providers, with teams, virtual keys, budgets, rate limits, routing and an admin
console built into a single Go binary. It is a **public alpha**. These principles
decide what "done" means for every change.

## Core Principles

### I. Governance fails closed

Budgets, authentication, RBAC, rate limits and concurrency caps are the product.
When they cannot be evaluated, the request is refused, never waved through.

- If accounting, reservation, or required Redis coordination is unavailable,
  deny with an explicit error (for example `503 accounting_unavailable`) rather
  than admit unmetered work.
- Missing token usage is not free inference. Unknown or unpriced usage is
  recorded as such and reserved conservatively; never shown as zero cost.
- A failed read of a balance, limit or policy returns an error, never a
  fabricated default such as a zero balance or "unlimited".
- Authorization is enforced server-side for every route, and role boundaries
  (`admin`, `manager`, `member`, service accounts) are covered by tests. Hiding a
  UI control is not an access control.
- Any behaviour that is deliberately fail-open must be named, justified and
  documented in the relevant `docs/` page.

### II. Everything is bounded

Every queue, buffer, retry, deadline, body size, worker pool, cache, label set
and in-memory collection has an explicit, documented limit.

- New limits get a default, a validation range, and a line in the docs.
- Deadlines compose: the outer request deadline (`server.v1_deadline`) always
  wins over inner timeouts, retries and fallback.
- Background work is cancelable and joined on shutdown; no unbounded goroutines,
  unbounded retries, or lossy in-memory queues for accounting data.
- Metrics labels come from bounded sets; never raw user input, keys or prompts.

### III. Compatibility is a contract

GateMux is a drop-in for OpenAI-compatible clients, and existing installations
must keep working.

- Stable inference endpoints (`/v1/chat/completions`, `/v1/embeddings`,
  `/v1/models`, SSE streaming) keep OpenAI wire compatibility; unknown request
  fields are forwarded losslessly on compatible backends.
- Breaking changes to Stable or Beta surfaces require a CHANGELOG entry and
  upgrade notes. Experimental surfaces may change, but the change is still noted.
- Legacy AIPort names (`AIPORT_*` environment variables, `X-Aiport-*` headers,
  `aiport_session` cookies, `aiport.*` storage keys) remain accepted until an
  explicit, announced removal.
- Schema migrations are forward-only, called out in the PR and CHANGELOG, and
  state any drain or single-owner requirement.
- Compose and the Helm chart default to the same published release version.

### IV. Claims match evidence

Public statements never outrun what is tested.

- Every feature has a maturity tier in `docs/supported-surface.md`
  (Stable, Beta, Experimental). A surface is promoted only with tests and docs
  that justify the new tier.
- Docs state what is implemented and what is not qualified, with the same care
  as `docs/public-alpha.md`. README and marketing copy may lead with benefits but
  must not claim more than the supported surface.
- No performance, capacity or security claims without a reproducible
  measurement or audit that is referenced from the docs.

### V. Tests prove behaviour

- Every behaviour change or bug fix ships with a regression test that fails
  without the change.
- Database and Redis behaviour is tested against disposable instances
  (`TEST_DATABASE_URL`, `GATEMUX_TEST_REDIS_ADDR`), never shared, preview or
  production data.
- Concurrency-sensitive packages keep passing `go test -race`
  (`internal/server`, `internal/api`, `internal/auth`, `internal/config`).
- Provider adapters are tested against local fake upstreams (`httptest`), with
  no real provider calls, credentials or paid requests in tests or CI.
- The console's pure logic and shared components have Vitest coverage; the
  isolated Docker smoke test (`scripts/smoke-compose.mjs`) keeps passing.

### VI. Secure and private by default

- No default secrets. A missing or empty admin key stops startup before any
  listener or database connection opens; a deployment without its provider
  credential is reported as such, never silently given a placeholder.
- Local stacks bind to loopback only. Production guidance assumes HTTPS,
  restricted networks and managed secrets (`docs/deploy.md`).
- Credentials, prompts, completions and request captures never appear in logs,
  error messages, URLs, commits, test fixtures, screenshots or issue text.
  Payload capture is opt-in per team and bounded.
- Secrets that are only verified are stored hashed (virtual keys, sessions,
  invite and reset tokens; passwords with bcrypt) and compared in constant time.
- Vulnerabilities are handled privately per `SECURITY.md`.

## Technical Constraints

- **One binary.** The gateway is Go (version per `go.mod`) with the React/Vite
  console embedded from `web/dist`. No additional runtime service is required
  beyond Postgres (state) and Redis (distributed coordination).
- **Dependencies are justified.** A new Go module or npm package needs a stated
  reason in the plan; prefer the standard library and existing dependencies.
- **Configuration** lives in `config.yaml` plus environment variables for
  secrets. New options are validated at load time with clear errors.
- **Providers** plug in behind `internal/providers` with declared capabilities;
  a new provider starts as Experimental.
- **Console** changes keep keyboard access, labelled controls, announced errors
  and both light and dark themes working.

## Development Workflow

- **Size the process to the change.** New features, behaviour changes, new
  providers and anything touching budgets, auth, routing or accounting go
  through Spec Kit: `/speckit-specify` → `/speckit-plan` → `/speckit-tasks` →
  `/speckit-implement`. Bug fixes, docs, dependency bumps and small
  good-first-issue changes use a normal PR.
- **Specs live with code** in `specs/<NNN-feature>/` and are committed in the
  same PR as the implementation.
- **Existing behaviour is specified by `docs/`.** Specs reference the relevant
  docs page instead of restating it, and update it when behaviour changes.
- **Every PR** completes `.github/pull_request_template.md`: user-visible change,
  verification commands, docs updated, migration/compatibility impact, and no
  credentials or captures.
- **Required checks:** `go test ./...`, `go vet ./...`, `npm --prefix web test`,
  `node --test scripts/rename.test.mjs`, and CI (`test-and-build`, `secrets`).
- **Releases** follow semantic versioning with `-alpha` / `-beta` suffixes and
  are cut by pushing a `v*` tag after CI passes on `main`.

## Governance

This constitution overrides conflicting guidance in plans, specs and tasks. The
"Constitution Check" in every plan lists which principles the change touches
and how each is satisfied; any violation is recorded in the plan's complexity
table with the simpler alternative that was rejected.

Amendments are made by pull request, with the reason in the description and a
Sync Impact Report at the top of this file. Versioning: MAJOR for removing or
redefining a principle, MINOR for adding a principle or section, PATCH for
wording. `CONTRIBUTING.md` and the PR template must stay consistent with this file.

**Version**: 1.0.0 | **Ratified**: 2026-09-30 | **Last Amended**: 2026-09-30
