# Contributing

Focused fixes, tests and documentation are welcome. Read the
[supported surface](docs/supported-surface.md) and [alpha limits](docs/public-alpha.md)
before expanding a feature. Keep public claims consistent with tested behavior.

## Development

Use Go 1.25 or newer, Node 24 LTS (minimum 22.13), Docker and Compose. Build the frontend before
Go tests or builds because the Go binary embeds `web/dist`.

```sh
npm --prefix web ci
npm --prefix web run build
go test ./...
go vet ./...
npm --prefix web test
node --test web/tests/auth-client.test.cjs
node --test scripts/rename.test.mjs
```

Database/Redis integration tests require an explicitly disposable database and
Redis instance. Never point tests at a preview, shared or production database:
some tests migrate and delete fixture data. Without these variables, dependent
tests skip and you have not run the integration suite.

```sh
docker compose -f deploy/test/docker-compose.yml up -d --wait
export TEST_DATABASE_URL='postgres://gatemux:gatemux@127.0.0.1:55434/gatemux?sslmode=disable'
export GATEMUX_TEST_REDIS_ADDR=127.0.0.1:56380
go test ./...
go test -race ./internal/server ./internal/api ./internal/auth ./internal/config
go vet ./...
docker compose -f deploy/test/docker-compose.yml down
```

The test stack uses tmpfs: stopping it discards its disposable test data.
CI runs these integration checks with its own service containers.

Browser smoke scripts are optional additional checks, not standalone unit tests:
they require Playwright plus an explicitly configured disposable running gateway
and fixtures. Do not point them at an existing preview by default. The isolated
Docker smoke test below checks startup, UI assets, admin access and mock inference
without paid provider calls:

```sh
node scripts/smoke-compose.mjs
```

If Playwright and its Chromium browser are installed, set
`GATEMUX_SMOKE_BROWSER=1` for additional master-key login, navigation and API-docs
browser checks against that isolated stack. `PLAYWRIGHT_MODULE` may point to
your separate Playwright installation. The scripts do not install it for you.

For the console, follow the [local quickstart](README.md). The Dockerfile builds
the frontend automatically. Run `docker build .` for packaging changes.

## Pull requests

- Explain the user-visible change, risk and verification commands.
- Include regression tests and update operator docs for public behavior changes.
- Call out schema migrations, compatibility changes and upgrade requirements.
- Keep queues, concurrency, memory, retries and labels explicitly bounded.
- Do not claim performance improvements without reproducible measurements.
- Do not commit local secrets, captures, generated output or dependency folders.
- Preserve license notices and verify you have rights to contribute.

Report bugs with a minimal reproduction, version, sanitized configuration and
expected/actual behavior. Never attach credentials, raw prompts or database dumps.
Use [private security reporting](SECURITY.md) for vulnerabilities.
