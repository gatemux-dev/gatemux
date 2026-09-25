# Changelog

## GateMux rename

- Canonical repository/module: `github.com/gatemux-dev/gatemux`; UI, commands,
  packages, containers, chart, metrics and documentation now use GateMux.
- Existing sessions, virtual keys, client headers and stored response IDs retain
  compatibility. Follow the [upgrade guide](docs/rename-upgrade.md) to preserve
  database/Redis identities and update monitoring. No data migration is performed.

## Unreleased — public alpha

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
