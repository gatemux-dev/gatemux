# GateMux

An open-source LLM gateway written in Go, with a built-in admin console.

GateMux gives teams one API for model access, virtual keys, routing, budgets,
rate limits, guardrails and usage reporting.

**Public alpha.** Suitable for local evaluation and controlled pilots. APIs and
configuration may change. Broad provider coverage, production-scale capacity
and multi-host recovery are not fully qualified. No performance-leadership
claim is made.

## What is included

- OpenAI-compatible chat completions, embeddings and model listing, including
  streaming and unknown-field forwarding on compatible backends.
- OpenAI, Anthropic, Azure OpenAI and OpenAI-compatible provider adapters.
- Virtual keys, teams, users, service accounts, RBAC, password and OIDC sign-in.
- Budget reservations, rate limits, bounded request admission and distributed
  concurrency controls.
- Routing, retries before the first byte, fallback and circuit breakers.
- Admin console for models, access, spend, requests, audit and literal-text guardrails.
- Docker Compose, Helm templates, an embedded API reference at `/docs`,
  and an OpenAPI document at `/openapi/v1.json`.

See the [supported surface](docs/supported-surface.md) for feature maturity and
[alpha limitations](docs/public-alpha.md) before using real traffic.

## Run locally

Upgrading an existing installation? Read the [rename upgrade guide](docs/rename-upgrade.md)
first to preserve your database volume and shared Redis namespace.

Requirements: Docker with Compose, and OpenSSL to generate an admin secret.
Clone the repository, then run from its root:

```sh
git clone https://github.com/gatemux-dev/gatemux.git
cd gatemux
export GATEMUX_ADMIN_KEY="$(openssl rand -hex 32)"
docker compose -f deploy/docker/docker-compose.yml up -d --build
```

Open **http://localhost:4000** and select **Emergency / break-glass admin key**.
Use the value of `GATEMUX_ADMIN_KEY` from your shell; keep it private. Save it in
your password manager if you want to reuse it. There is no default admin key.

The console works without a paid provider key. Inference requires a real
configured provider: set `OPENAI_API_KEY` before starting to use the sample
`gpt-4` alias, or configure your own deployment and alias in the console.
Provider calls may incur charges. The sample alias is only a local example;
verify that your provider account supports its configured upstream model.

All published ports bind to loopback: gateway 4000, Postgres 55432, Redis 56379.
Set `GATEMUX_PORT=4001` if the gateway port is busy. Development SSO is opt-in;
see [deployment](docs/deploy.md). This stack is not an internet-facing production
configuration.

In the console, create a team and virtual key. Use the virtual key for inference,
not the admin key:

```sh
# Set GATEMUX_VIRTUAL_KEY to the newly issued virtual key in your shell.
curl --fail-with-body http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer $GATEMUX_VIRTUAL_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Hello"}]}'
```

Stop without deleting database state:

```sh
docker compose -f deploy/docker/docker-compose.yml stop
```

## Documentation

- [Supported surface](docs/supported-surface.md)
- [Alpha limitations and roadmap](docs/public-alpha.md)
- [Deployment, security and upgrades](docs/deploy.md)
- [API compatibility](docs/api-compatibility.md)
- [Configuration example](examples/config.yaml)
- [vLLM example and setup](docs/vllm.md)
- [Contributing and tests](CONTRIBUTING.md)
- [Security reporting](SECURITY.md)
- [Changelog](CHANGELOG.md)

## License

[MIT](LICENSE). Contributors must have the rights to submit their work.
