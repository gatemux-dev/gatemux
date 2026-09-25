# Security policy

GateMux is a public alpha, not a security-audited production release. Security
fixes currently target the default branch; there is no long-term support line.

## Report a vulnerability privately

Use GitHub's **Security → Report a vulnerability** on the
[official project repository](https://github.com/gatemux-dev/gatemux/security).
Do not disclose exploits or secrets in public issues or pull requests.

If that private-report button is unavailable, do not post the details publicly:
ask a maintainer to enable private vulnerability reporting through a non-sensitive
issue. Maintainers must enable this setting before publishing the repository.

Include the affected version/commit, endpoint, configuration prerequisites,
impact and a minimal sanitized reproduction. Never send live credentials,
customer prompts or database dumps. Rotate any exposed credential immediately.

Maintainers aim to acknowledge and investigate reports promptly, coordinate a
fix or mitigation, and agree on disclosure. No response-time SLA is offered.

## Operator responsibilities

Follow the [deployment security checklist](docs/deploy.md): HTTPS, Secure cookies,
restricted network exposure, secret rotation, verified admin identity, backups,
and reviewed telemetry/retention. Use `admin.disable_master_key` to reject
master-key authentication after bootstrap; hiding its UI alone is insufficient.

See [alpha limitations](docs/public-alpha.md) for known qualification gaps.
