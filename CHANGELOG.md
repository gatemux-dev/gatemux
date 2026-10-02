# Changelog

## GateMux rename

- Canonical repository/module: `github.com/gatemux-dev/gatemux`; UI, commands,
  packages, containers, chart, metrics and documentation now use GateMux.
- Existing sessions, virtual keys, client headers and stored response IDs retain
  compatibility. Follow the [upgrade guide](docs/rename-upgrade.md) to preserve
  database/Redis identities and update monitoring. No data migration is performed.

## v0.2.0-alpha — 2026-10-02

- The Compose quickstart pulls the published image instead of building from
  source (`GATEMUX_VERSION` selects a release); `docker-compose.build.yml`
  keeps the source build for contributors.
- **Exact sub-cent request costs (behaviour change, COST-003).** Costs are now
  recorded exactly in micro-cents (1/1,000,000 cent) instead of being rounded up
  to whole cents per direction. Cents values drop for small requests: 100 input
  and 50 output tokens at 10/40 cents per million cost $0.00003 instead of 2 cents.
  Budget admission compares exact spend with whole-cent limits, so sub-cent
  remainders are usable, and every error path still refuses.
- Existing `*_cents` fields keep their names and integer types and now report the
  exact amount rounded up; a positive amount is never 0. New additive fields:
  `cost_microcents` and `cost_precision` (`exact` or `whole_cent`) on usage rows,
  `includes_whole_cent_history` and `cost_microcents` on spend totals,
  aggregates and timeseries (plus `aliases_microcents`), and
  `spend_so_far_microcents`, `projected_microcents`, `used_microcents`,
  `period_spend_microcents`, alert `spend_microcents`/`limit_microcents` and
  callback `cost_microcents`. Micro-cent values are base-10 JSON strings.
- The usage CSV appends `cost_microcents,cost_precision`; existing columns keep
  their order.
- `/me/usage` rows now include `accounting_state`, so My usage shows unknown and
  unpriced costs as words.
- `gatemux_cost_cents_total` now increments by fractional cents; its name and
  labels are unchanged.
- The console shows exact amounts such as `$0.00003`; limits stay whole cents.
- Accessibility: Signup announces loading and errors to screen readers, and
  form fields link their hints and errors to the control and mark it invalid,
  visually and for assistive technology.
- Dependencies: `golang.org/x/oauth2` 0.37.0; Bedrock runtime SDK 1.63.0.
- Security hardening found by code scanning: the OIDC sign-in error page uses the
  standard HTML escaper, and expiring OIDC state cookies use the same `Secure`
  and `SameSite` policy as setting them.

### Upgrade notes

- **Back up Postgres before upgrading.** Migrations run automatically when the
  gateway starts, so with Compose, pulling this repository and restarting
  upgrades to the pinned release and applies 0041 immediately.
- Migration `0041_exact_microcents` is forward-only. Run it with inference
  drained and a single migration owner, as for 0039. It adds nullable
  micro-cent columns (existing usage is not rewritten and is reported as
  `whole_cent` history) and switches `budget_daily_totals` to micro-cents.
- After 0041, older binaries fail their budget reads and refuse budgeted
  requests (fail closed). Do not serve traffic from older binaries after it runs.
- `0041_exact_microcents.down.sql` exists for manual disaster recovery only; the
  migration runner never executes it.

## v0.1.0-alpha — 2026-09-26

First tagged public alpha.

- Release artifacts: linux/darwin amd64/arm64 binaries and a multi-arch
  `ghcr.io/gatemux-dev/gatemux` image; the Helm chart now defaults to this image.
- vLLM configuration example and guide.
- Fixed Compose compatibility when only `GATEMUX_ADMIN_KEY` is set; retained
  legacy-key fallback and added missing-key startup regression checks.
- Go gateway with embedded admin console, compatible chat/embeddings, streaming,
  provider routing, retries and fallback.
- Team and virtual-key access, accounts, OIDC, budgets, rates and concurrency.
- Literal-text guardrails, usage/spend reporting, audit and durable accounting.
- Local Compose and Helm templates with explicit alpha limitations.
- Publication hardening: loopback-only local ports, required admin secret,
  opt-in development SSO, bounded/shared authentication throttling, cookie-only
  password sessions and server-side master-key disable policy.
- Configurable Secure cookies for HTTPS-terminating proxies.
- Updated browser routing and frontend build/test dependencies after advisory
  scanning; Node 24 LTS is used for Docker and CI frontend builds.
- Console improvements: searchable pickers, command menu, server-filtered request
  logs, spend breakdowns, pricing imports and clearer captured-conversation details.
- Reversible key pause, rotation grace periods and configuration provenance.
- Fixed pause/rotation interleaving: paused state is checked under the transaction
  lock before a replacement key can be issued.
- Documented GateMux JSON price-list schema with required provider identity and
  validated rates; imports cannot apply one provider's prices to another.

### Compatibility notes

Password login no longer returns a token in JSON; clients must retain the
HttpOnly session cookie. Authentication limits count successful and failed
attempts. Redis failure fails closed for password login/reset. The old
`disable_master_key_login` setting remains UI-only; use `disable_master_key`
for enforcement.

Manual price imports require the `provider` field and the schema in
[token pricing](docs/token-pricing.md). Convert other formats explicitly before
importing. Key pause uses migration 0040; migration 0039's earlier backfill/drain
requirements still apply when upgrading an older database.

This is not a stable production release or a claim of universal API/provider
compatibility. Read the [supported surface](docs/supported-surface.md),
[limitations](docs/public-alpha.md) and [upgrade instructions](docs/deploy.md).
