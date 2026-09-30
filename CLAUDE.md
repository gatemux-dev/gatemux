# GateMux: guide for Claude Code

GateMux is an open-source LLM gateway in Go: one OpenAI-compatible API in front of
many providers, with teams, virtual keys, budgets, rate limits, routing and an
embedded React admin console. It is a public alpha.

**The project rules are in [`.specify/memory/constitution.md`](.specify/memory/constitution.md).**
Read it before planning or changing behaviour. In short: governance fails closed,
everything is bounded, compatibility is a contract, claims match evidence,
tests prove behaviour, secure and private by default.

## Layout

- `cmd/gatemux`: the gateway binary (`serve`, `version`); `cmd/mock-openai`: synthetic upstream for tests
- `internal/api`: HTTP handlers (inference `/v1/*`, admin `/admin/*`, `/me/*`)
- `internal/server`: routing, middleware, startup and shutdown
- `internal/auth`: keys, sessions, OIDC, RBAC
- `internal/budget`, `internal/ratelimit`, `internal/concurrency`, `internal/admission`: cost and load controls
- `internal/router`, `internal/providers/*`: routing, fallback and provider adapters
- `internal/store`: Postgres access and migrations
- `web/`: React/Vite console, embedded into the binary from `web/dist`
- `deploy/`: Compose (`docker/`), Helm chart (`helm/`), smoke and test stacks
- `docs/`: the specification of current behaviour; update it when behaviour changes
- `specs/`: Spec Kit feature specs, plans and tasks

## Commands

Build the console before Go tests or builds (the binary embeds `web/dist`):

```sh
npm --prefix web ci && npm --prefix web run build
go test ./... && go vet ./...
npm --prefix web test && npm --prefix web run lint
node --test scripts/rename.test.mjs
```

Integration tests need a disposable Postgres and Redis. Never point them at real data:

```sh
docker compose -f deploy/test/docker-compose.yml up -d --wait
export TEST_DATABASE_URL='postgres://gatemux:gatemux@127.0.0.1:55434/gatemux?sslmode=disable'
export GATEMUX_TEST_REDIS_ADDR=127.0.0.1:56380
go test ./... && go test -race ./internal/server ./internal/api ./internal/auth ./internal/config
docker compose -f deploy/test/docker-compose.yml down
```

Docker smoke test (no provider calls): `node scripts/smoke-compose.mjs`.

## How to work

- **Features and behaviour changes** (anything touching budgets, auth, routing,
  accounting, providers or public APIs): use Spec Kit.
  `/speckit-specify` → `/speckit-clarify` (optional) → `/speckit-plan` →
  `/speckit-tasks` → `/speckit-analyze` (optional) → `/speckit-implement`.
- **Bug fixes, docs, dependency bumps, small UI fixes**: a normal branch and PR,
  with a regression test that fails without the fix.
- Match the surrounding code's style, comment density and naming. Keep diffs
  focused; no unrelated reformatting.
- Update the relevant `docs/` page, `docs/supported-surface.md` tier and
  `CHANGELOG.md` when public behaviour changes.
- Fill in `.github/pull_request_template.md` with the exact commands you ran.

## Never

- Commit or print credentials, prompts, request captures or database dumps.
- Call real providers or use paid API keys in tests.
- Add unbounded queues, retries, goroutines or metric labels.
- Claim performance, capacity or security properties without evidence in the docs.
- Break legacy `AIPORT_*` / `X-Aiport-*` compatibility without an announced removal.
