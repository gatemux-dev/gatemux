# Embeddings and token-tier accounting

Implemented contract, 2026-09-07. Prices are operator-configured integer USD cents
per million tokens, not a live provider price feed. Migration 0035 adds pricing
tiers and payload-free usage details; existing rows inherit their base rates.

## Embedding input

`POST /v1/embeddings` accepts a nonempty string, nonempty string array, nonempty
array of nonnegative integer token IDs, or a nonempty array of those token arrays.
Nulls, empty items, mixed shapes, negative/fractional/overflowing IDs are rejected.
Token-array admission counts IDs exactly, not their decimal characters. Compatible
adapters retain input shape, options and raw numeric fidelity when replacing the
model alias. Gemini and Cohere reject tokenizer-specific IDs with HTTP 400 before
network IO; callers must use text or a compatible deployment. Provider-specific
context length, batch size, tokenizer and dimensional limits remain upstream rules.

## Price semantics

`POST /admin/pricing` creates a new effective full price row. Optional fields:

| JSON field | Inheritance when omitted/null |
| --- | --- |
| `cache_read_per_million_cents` | Base input |
| `cache_write_per_million_cents` | Base input |
| `cache_write_1h_per_million_cents` | General write, then base input |
| `reasoning_per_million_cents` | Base output |

Explicit zero is free, not inheritance. Negative and non-integer rates are rejected.
The Spend & usage pricing form supports editing all rates and clearing overrides.
Input totals include reads/writes; output totals include reasoning. The calculator
partitions these totals into disjoint tiers, sums exact integer products, rounds
up **once per direction** to whole cents, then adds the two directions. It neither
adds cached/reasoning counts to inclusive totals nor rounds each tier separately.
This intentionally preserves legacy whole-cent accounting; it is not fractional
cent billing and may differ substantially from provider invoices for tiny calls.

Admission reserves the highest configured input/output tier across eligible
fallbacks. Settlement uses actual details. Inconsistent/negative counts or cost
overflow fail computation and retain the reservation estimate through idempotent
conservative settlement. There is no automatic model-price feed. Audio/image
token rates, batch/service-tier discounts and provider cache-storage charges are
not modeled. Cost-based routing still orders by base input price, not predicted
cache hits or reasoning. Missing provider usage cannot produce an exact invoice.
The [durable accounting](durable-accounting.md) journal preserves interrupted
requests and exposes priced/estimated/unknown/unpriced evidence in usage and CSV;
explicit zero counters remain distinct from missing or partial usage.

## Manual price-list import

Models → Pricing accepts an operator-supplied GateMux JSON price list. The object
is keyed by upstream model name, or by `provider/model` when names overlap:

```json
{
  "example-model": {
    "provider": "openai",
    "input_cost_per_token": 0.000001,
    "output_cost_per_token": 0.000002,
    "cache_read_input_token_cost": 0.0000005
  }
}
```

These are illustrative rates, not current provider prices. `provider` is required
and must exactly match the deployment's gateway provider type, such as `openai`,
`azure_openai` or `openai_compatible`. Missing provider identity is rejected,
never treated as a wildcard. Both input/output costs are required; cache-read
and `cache_creation_input_token_cost` rates are optional. Rates must be finite,
nonnegative numbers whose converted cents-per-million value is a safe integer.
The preview rounds per-token USD × 100,000,000 to whole cents per million.

Only deployments loaded in the pricing workspace are matched (currently at most
500). Review the preview before applying; unmatched models remain unchanged.
Imports use sequential price writes, not an atomic batch: a failure reports how
many writes succeeded. Omitted cache-read/write rates clear those overrides;
existing one-hour-write and reasoning overrides are preserved. No remote catalog
is fetched. Other JSON formats must be converted to this documented schema first.

## Native usage normalization

- OpenAI-compatible cached and reasoning details are inclusive subsets.
- Anthropic and Bedrock cache read/write counts are outside native input totals;
  adapters add them once, retaining 5-minute/1-hour write details.
- Gemini/Vertex prompt totals already include cached tokens; thought counts are
  added once to candidate output totals and retained as reasoning details.
- Direct/Vertex Gemini, Anthropic/Vertex Anthropic, and Bedrock streams now emit
  canonical usage metadata and reject malformed/truncated upstream completion.
- `usage_log.token_details` retains canonical counts only, without prompts or
  generated text, and the admin usage response exposes `token_details`.

Tests use local HTTP fixtures, disposable Postgres and Redis. They do not qualify
live cloud credentials, all provider model variants, or provider billing invoices.

Protocol sources: [OpenAI embeddings](https://developers.openai.com/api/reference/ruby/resources/embeddings/methods/create),
[OpenAI prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching),
[OpenAI reasoning](https://developers.openai.com/api/docs/guides/reasoning),
[Anthropic cache usage](https://platform.claude.com/docs/en/build-with-claude/prompt-caching),
[Gemini usage metadata](https://ai.google.dev/api/generate-content#UsageMetadata),
and [Bedrock cache totals](https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html).
