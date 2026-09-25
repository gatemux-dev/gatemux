# Public alpha: scope and next steps

GateMux is ready for evaluation and feedback, not a blanket production-readiness
promise. “Stable” in the [surface matrix](supported-surface.md) describes the
best-tested feature contract within this alpha; it does not make the product GA.

## Known limits

- Provider adapters and endpoint maturity differ. Native Responses is a bounded
  subset; audio, images, rerank and some provider adapters are experimental.
- Guardrails inspect a restricted plain-text chat contract using literal terms.
  They are not general PII detection or comprehensive safety moderation.
- Budget admission uses reservations and estimates where exact provider usage
  is unavailable. Not all modalities, pricing dimensions or opaque proxy routes
  are metered. Cost totals are not a provider invoice reconciliation guarantee.
- Distributed controls need the documented Redis configuration. Local fallback
  behavior differs by subsystem; do not infer universal fail-open or fail-closed
  behavior. Login/reset coordination fails closed when configured Redis fails.
- Local fault and soak tests do not qualify multi-host deployments, Redis Cluster,
  Kubernetes rolling upgrades, internet-scale capacity or every cloud provider.
- Migration 0039 requires a drained, single-owner backfill. Historical retention
  and budget semantics are described in [budget totals](budget-daily-totals.md).
- Control-plane OpenAPI schemas are not a complete field-by-field contract.
- No independent security audit, capacity guarantee or performance-leadership
  claim is offered. Use trusted networks and non-sensitive evaluation data first.

## Roadmap

1. Broaden authorization/isolation, concurrent accounting and recovery coverage.
2. Qualify deployment topologies, upgrades, restore procedures and realistic load.
3. Gather controlled pilot feedback and improve operator workflows and docs.
4. Extend providers/modalities only with explicit compatibility and accounting tests.

Please submit focused reproductions without credentials or customer data.
See [contributing](../CONTRIBUTING.md) and [security reporting](../SECURITY.md).
