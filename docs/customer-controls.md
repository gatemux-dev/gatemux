# Customer controls

Implemented subset, 2026-09-08. Customers are team-scoped application identities,
not authenticated end users. A trusted application holding a virtual key must
supply the identity; a caller able to invent IDs can evade per-customer quotas.

## Registration and identity

Team → Customers controls `customer_registration`:

- `optional` (default): identity is optional; unknown supplied IDs are logged but
  do not create database rows or acquire customer limits.
- `required`: a supplied, pre-registered, active customer is mandatory.
- `auto_create`: identity is mandatory; a previously unseen ID is registered once
  without resetting existing policy. Automatic registration stops at 10,000 rows
  per team, including archived rows. Existing customers continue working.

Precedence is `X-Gatemux-Customer-Id`, `X-Customer-Id`, then the request `user` field.
Opaque passthrough supports headers only. IDs are 1–256 bytes, without surrounding
whitespace, control characters or slashes. Archived IDs are denied. Resolution is
fresh from Postgres per identified request, under a two-second timeout; policy
lookup failures deny admission. This intentionally trades a DB read for prompt
policy revocation; it is not a claim of a metadata-cache-only hot path.

## Admission and accounting

Customer concurrency, RPM, TPM and budgets apply across a team's keys and gateway
replicas. Concurrency uses the existing expiring Redis lease mechanism. RPM/TPM
share one atomic Redis operation with team/key rates; a denied scope does not
consume the other scopes. These fixed-minute counters use estimated input plus
reserved output, not actual-token reconciliation. Zero/blank rates mean unlimited.
Redis Cluster is not qualified; production distributed limits require Redis, not
the bounded in-memory development fallback.

Budgets are integer cents, UTC calendar day/month. Blank is unlimited; zero blocks
positive-cost work. Ordered advisory locks and reservations prevent simultaneous
requests from overselling the *estimated* budget. Accepted work settles once with
authoritative token pricing. Missing streamed usage conservatively retains the
estimate, including completed streams that omitted their usage event. Actual
provider usage can exceed estimates; this is not a guarantee against every
provider-side overrun. Process-crash reservation reconciliation remains open.

Chat budget/TPM customers get an explicit 1024 output-token cap if omitted.
`max_completion_tokens` is the default; clients of older compatible upstreams can
explicitly supply `max_tokens`. Supplying both is rejected. `n` choices multiply
the output estimate (1–128 choices, 1–1,000,000 tokens each). Translating adapters
read the effective cap. The modern field includes reasoning tokens; the legacy
field is incompatible with OpenAI o-series models. See the
[official Chat Completions reference](https://developers.openai.com/api/reference/cli/resources/chat/subresources/completions/methods/create).

Chat, embeddings and native Responses use customer reservations and usage
attribution. Native/opaque endpoints without token accounting reject customers
with a budget or positive TPM; RPM and concurrency still apply otherwise.
Customer budgets also reject explicit audio, hosted web search, prediction and
non-default service-tier options whose costs are absent from the price table.
See [token pricing](token-pricing.md) for remaining model/modality limits.

Cache hits count toward rate/concurrency admission, record customer attribution,
cost zero and do not reserve new budget. Budget summaries combine the reservation
ledger with historical customer usage that has no matching ledger row, avoiding
double counting. New requests use synchronous atomic completion and a durable
pre-call journal; interruption retains estimates or explicitly unknown cost.
See [durable accounting](durable-accounting.md), including upgrade limits. Native
audit rows explicitly mark `token_details.accounting=unpriced_native`; opaque
routes use `unpriced_passthrough`. A zero cost there does not mean free inference.

## Operator APIs

- `PATCH /admin/teams/{slug}/customer-policy`: `customer_registration`.
- `POST /admin/teams/{slug}/customers`: registration with optional limits.
- `PATCH /admin/teams/{slug}/customers/{externalID}`: partial name, budget, period,
  RPM and TPM edits. Omitted preserves; explicit `null` clears a nullable limit.
- Existing customer concurrency endpoint edits the separate concurrency policy.
- `GET /admin/teams/{slug}/customers/{externalID}/budget`: used/reserved summary.
- `GET /admin/spend?team={slug}&customer={externalID}`: recorded customer spend;
  a customer filter requires a team filter and the caller's team access.

Managers can operate only their own team. Team → Customers provides registration
policy, budget/rate editing and used/reserved versus recorded-spend summaries.
Live manager directory dependencies are tracked separately in milestone 6.

## Verification

Migration 0037; API tests cover registration modes, archive denial, cross-team
authorization, partial edits/null/zero semantics, 16-way registration and budget
contention, auto-registration cap, persisted attribution, unpriced rejection,
missing-stream-usage settlement and cross-key native/chat RPM. Real Redis tests
use two limiter instances and 64 competing keys to verify atomic scope denial.
Full Go/race/vet and official SDK contracts pass against disposable services.
The browser fixture covers policy editing, customer spend, mobile form and retry;
its preview writes are intercepted, not live manager authorization evidence.
