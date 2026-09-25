# Team manager workflows

Implemented and verified 2026-09-12. This is a focused pilot slice, not complete
enterprise delegation or a claim that the whole pilot is ready.

## Authorization boundary

| Surface | Manager | Administrator |
| --- | --- | --- |
| Team, keys, customers, budget/concurrency | Own team only | All teams |
| Member directory and model-name catalog | Own team only | Selected team |
| Invitations | List own team; invite members into own team | Global list and role/team choice |
| Spend and request metadata | Own team only | Global or filtered |
| Service accounts | List/register for own team | Also update/archive/issue keys |
| Pricing, projections, global users/deployments | No access | Allowed |
| Payload capture, stored payloads/replay, full effective policy | Read capture setting only | Allowed |

Members cannot access these team-management APIs. Ordinary members can enter an
alias manually in Playground; inference still requires a valid virtual key and
enforces that key's current policy. Managers get team-scoped alias suggestions.
Role-aware UI is explanatory, never a replacement for backend authorization.

## Scoped directory APIs

- `GET /admin/teams/{slug}/members?limit=50&offset=0&q=alice`
- `GET /admin/teams/{slug}/models?limit=50&offset=0`

Both use existing session/master-key authentication and team-access middleware.
Each returns a JSON array and `X-Total-Count`. Limits default to 50 and clamp to
500; negative offsets normalize to zero. Filtering happens in SQL **before**
pagination, including the count. An empty/out-of-range page still reports the
scoped total (counts and pages may change as concurrent edits occur).

Members expose only `id`, `email`, `name`, `role`, optional `last_login_at` and
`disabled_at`. Archived users are omitted; disabled users remain identifiable.
No global user budgets, OIDC metadata, password hashes or session data are
returned. Search is case-insensitive literal name/email substring matching,
limited to 256 UTF-8 bytes; `%` and `_` are not search wildcards.

Models expose only `alias`. Team allowlists apply before pagination; empty or
`*` policies include all registered aliases. Deployment names, URLs, credentials,
routing and pricing are not exposed. A listed alias is not a guarantee of current
deployment availability. Key-specific model restrictions only narrow team policy.

Manager team and invitation lists also apply scope before pagination. A manager
without a team receives empty lists, never the global directory.

## Console behavior and current limits

Team members and key-owner selection are paginated; key owners are searchable.
Only directory members can be selected, and backend key creation independently
checks team membership. Unavailable team/spend data gets an explicit error and
retry, not a fabricated zero-spend or unlimited-budget summary. Team navigation
remounts state; late responses cannot overwrite a newer team-page load.

The key model picker lists at most 500 allowed models and reports truncation.
Playground offers up to 200 suggestions plus manual alias entry. Spend's user
filter lists up to 500 directory users and reports truncation; all-user spend
still includes everyone in the authorized scope. These are explicit UI bounds,
not hidden limits on inference or accounting.

## Verification

`internal/api/team_directory_test.go` covers role/tenant denials, field projection,
pre-pagination scoping/counts, directory search, archived/disabled visibility,
model policy and foreign key-owner rejection.

`internal/server/manager_live_test.go` + `web/tests/manager.live.cjs` use actual
server routes, embedded UI, password logins and HttpOnly cookies. No auth/API
interception. The fixture creates a random isolated schema in the explicitly
provided disposable database and removes only that schema after the test. No
providers are configured and no requests incur cloud cost.

```sh
npm run build --prefix web
TEST_DATABASE_URL='postgres://gatemux:gatemux@127.0.0.1:55434/gatemux?sslmode=disable' \
GATEMUX_TEST_REDIS_ADDR=127.0.0.1:56380 \
GATEMUX_MANAGER_BROWSER_TEST=1 \
PLAYWRIGHT_MODULE=/path/to/node_modules/playwright \
go test ./internal/server -run '^TestManagerLiveBrowser$' -count=1 -v -timeout 180s
```

The browser test checks real key issuance/rotation/revocation and customer policy
writes, own-team spend, member pagination/search, permitted model names, payload/
pricing restrictions, invite constraints, member/foreign-team denials, explicit
foreign-team error UI and mobile layout. The Go fixture checks persisted results.
The broader guardrail, crash-reconciliation and recovery gates remain in
[alpha limitations](public-alpha.md).
