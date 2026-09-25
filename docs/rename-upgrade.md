# GateMux naming and existing-installation upgrades

The project is GateMux. Its canonical repository and Go module are
`github.com/gatemux-dev/gatemux`; commands are `gatemux` and `gatemux-load`,
and the Helm chart is `deploy/helm/gatemux`. Registry examples use
`ghcr.io/gatemux-dev/gatemux`; this is a target name, not a published-image promise.
The local checkout directory may keep its old name without affecting builds.

## Existing data is not renamed

Do not start the fresh-install Compose defaults over an existing AIport stack.
New samples use database/user/volume names and an explicit Redis prefix of
`gatemux`. Pointing them at a different volume creates an empty database; changing
Postgres initialization variables does not rename an existing database or user.

Before upgrading:

1. Inspect the running stack's exact project name, mounted database volume,
   database credentials, Redis database/prefix, configuration and image. Back up
   the database and test restoring it. Keep the old image/config for rollback.
2. Preserve the existing DSN, Redis database and `key_prefix` on every replica.
   The runtime's omitted Redis prefix intentionally remains `aiport` for old
   configurations. Never split live replicas between prefixes: limits, leases,
   breakers and login throttling would stop sharing state.
3. Drain and stop the old gateway writers before starting GateMux. The Compose
   service is now named `gatemux`, not `aiport`; an old gateway can otherwise
   remain running as an orphan. Do not use `down --volumes` or assume mixed-version
   rolling upgrades are qualified. Follow the [migration rules](deploy.md#upgrades).
4. Adapt the opt-in `deploy/docker/legacy-compose.yaml` and `legacy-config.yaml`
   to the inspected deployment. They retain the original development database
   identity and Redis namespace, and map the new volume handle to an explicitly
   named **external existing volume**. Missing volumes fail rather than being
   silently initialized. Preserve all private configuration and provider secrets.
5. Keep the old Compose project/network and desired listening port explicit.
   Validate the merged configuration before a separately planned upgrade:

   ```sh
   # Example only: replace these with the inspected deployment identities.
   export GATEMUX_LEGACY_PG_VOLUME=docker_aiport_pg_data
   export GATEMUX_PORT=44000
   # Supply GATEMUX_ADMIN_KEY securely; do not print the rendered secrets.
   docker compose -p docker -f deploy/docker/docker-compose.yml \
     -f deploy/docker/legacy-compose.yaml config --quiet
   ```

This rename does not migrate schemas or rewrite users, budgets, keys, usage,
stored responses, object archives, volumes or historical benchmark evidence.
Existing `gw-` virtual keys remain unchanged.

## Compatibility

- New sessions use the HttpOnly `gatemux_session` cookie. Existing
  `aiport_session` cookies remain accepted with the same database validation and
  RBAC. The new cookie takes precedence; cookies still precede bearer tokens.
  Login clears the old cookie; logout clears both and revokes both supplied
  browser session identities. No account token is moved into JavaScript storage.
- Existing same-origin `aiport.masterKey` session storage moves once into
  `gatemux.masterKey`; existing theme preferences are retained. Credentials stay
  in session storage, not persistent local storage. Old local-storage cleanup
  remains in place. Changing the origin/port still requires signing in again.
- New OIDC cookies use `gatemux_oidc_*`; an in-flight legacy state/nonce pair is
  accepted without mixing names. Preserve your registered OIDC client/redirect
  configuration when upgrading; fresh development Dex samples use new names.
- Use `GATEMUX_ADMIN_KEY` and `GATEMUX_OTEL_STDOUT`. The binary accepts their
  `AIPORT_*` equivalents when the new variable is unset; explicitly empty new
  values do not fall back. Old explicit `admin.master_key_env` names still work.
  Provider credential references are literal and unchanged. Compose also accepts
  old admin-key and port variables; test/SDK script flags now use `GATEMUX_*`.
- Use `X-Gatemux-Tags`, `X-Gatemux-Region`, `X-Gatemux-Customer-Id` and
  `X-Gatemux-No-Cache`. Old `X-Aiport-*` aliases remain accepted. A nonempty new
  header wins conflicts, except no-cache, where either value of `1` bypasses
  caching. Cache hits emit both header names. Customer identity is still a
  trusted client assertion, not customer authentication.
- New owned Responses IDs start with `resp_gatemux_`; existing `resp_aiport_`
  IDs remain usable subject to the same ownership and retention checks.
- Metrics now use `gatemux_*`, default trace service/instrumentation names use
  `gatemux`, and the bundled dashboard is updated. Update custom queries/alerts
  together with the binary; old metric series are not dual-emitted. Historical
  monitoring data and object-storage archives are not renamed.

## Existing Helm releases

Keep the existing release name/namespace and set `nameOverride` to the old chart
name (`aiport` for defaults). Preserve any existing `fullnameOverride` and service
account identity. These keep resource names and immutable selector labels stable
while the new chart uses GateMux internally. Preserve the DSN, Redis prefix,
secrets, ingress, registered OIDC client and storage identities in private values.
Render/diff the chart before upgrading; this is not rolling-upgrade qualification.
