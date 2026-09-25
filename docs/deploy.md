# Deployment

GateMux is a public alpha. Start with a local evaluation and qualify your own
traffic and failure modes before handling sensitive or production workloads.

## Local Compose

Follow the [README](../README.md). Set a strong `GATEMUX_ADMIN_KEY` before every
Compose command. Postgres data is stored in the named `gatemux_pg_data` volume;
`stop` preserves it. Do not use `down --volumes` on data you want to keep.

The sample database credentials and provider placeholder are for a loopback-only
development stack. Replace them and use managed secrets for deployment. Never
commit real credentials, local configuration, database dumps or request captures.

The `oidc-dev` profile starts a development identity provider with public fixture
credentials. It is disabled by default. Enabling the profile alone does not
enable SSO: configure an issuer reachable by both browser and gateway, client
secret, and matching redirect URLs in `config.yaml` and `dex.yaml`. Do not use
this development identity provider for real users.

## Security checklist

1. Put the gateway behind HTTPS and network access controls. Do not publish
   Postgres or Redis to the internet.
2. Set `admin.secure_cookies: true` behind TLS termination. This marks password,
   invite and OIDC session cookies, and OIDC state cookies, Secure even when
   the proxy connects to the gateway over HTTP. Forwarded headers do not
   decide this setting. Leave it false only for local HTTP evaluation.
3. Configure `server.trusted_proxies` with the exact proxy CIDRs, not all
   addresses. Apply connection/request limits at the edge as well.
4. Bootstrap an administrator account through an invite or verified OIDC role
   mapping. Confirm account login works, then set
   `admin.disable_master_key: true` and restart. This rejects master-key bearer
   authentication server-side, including drain routes. Keep the configured
   master-key environment variable populated for startup validation.
   `disable_master_key_login` only hides the UI; it is not an authorization control.
5. Review provider pricing and budget limits before sending paid requests.
   Not all routes or cost dimensions can be metered. See
   [key budgets](key-budgets.md) and [token pricing](token-pricing.md).
6. Review telemetry, payload capture and retention. Avoid sensitive prompts
   until you have verified the applicable privacy controls.
7. Configure backups, restore tests, monitoring and an upgrade procedure.

## Authentication limits

Password login reserves attempts before verification: 20 per source IP and
5 per normalized account per 15-minute UTC fixed window, including successful
attempts. Password reset has a separate 20/IP window. Exceeding a limit returns
429 with `Retry-After`. Shared Redis counters contain hashed identities and
expire at the window boundary; each window is capped at 10,000 fields.
Redis errors or counter-capacity exhaustion return 503, without falling back
to weaker limits. Without Redis the same policy is bounded but process-local.
Replicas must share Redis/prefix and have synchronized clocks. This does not
replace edge-level DDoS protection.

Password login returns user information and an HttpOnly cookie, not a session
token in JSON. Password/invite sessions use SameSite=Strict; OIDC uses Lax to
support the redirect flow.

## Health and shutdown

`/healthz` reports process health; `/readyz` reports readiness.
Administrator-only `POST /health/drain` starts irreversible drain until restart.
Container stop time must exceed `server.shutdown.grace_period` plus
`cleanup_timeout` (sample: 30s + 10s, with 45s container grace).

See [shutdown and recovery](shutdown-recovery.md) for exact deadlines and
[durable accounting](durable-accounting.md) for interruption semantics.

## Upgrades

For existing pre-rename installations, follow the [GateMux upgrade guide](rename-upgrade.md)
before using the fresh-install samples. Preserve database and Redis identities.

Back up and test a restore before upgrades. Schema migrations run at startup.
Migration 0039 backfills historical budget totals: drain all writers and run
a single migration owner before starting replicas. Do not assume mixed-version
rolling upgrades are safe. Follow [bounded budget reads](budget-daily-totals.md)
and the accounting contract for retention and historical-data restrictions.

## Kubernetes

The chart at `deploy/helm/gatemux` is a starting template, not a production
qualification. Build and publish your own image to a registry you control;
override `image.repository` and `image.tag`. No prebuilt public image is promised.

Supply external Postgres and Redis, secret-backed environment variables through
`envFrom`, HTTPS ingress, proxy CIDRs, cookie policy and resource limits.
Database DSN values are literal YAML strings: do not expect shell interpolation.
Use a privately rendered values file or your deployment secret tooling.

```sh
helm upgrade --install gatemux deploy/helm/gatemux \
  --namespace gatemux --create-namespace -f your-private-values.yaml
```

Multi-host, Redis Cluster, rolling-upgrade and capacity qualification remain open.
