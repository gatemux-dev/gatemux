# API reference and native Responses contract

Implemented 2026-09-07. This is a supported subset, not complete OpenAI
API parity. No paid upstream or cloud SDK calls were used for qualification.

## Reference and SDK checks

`/openapi/v1.json` (also `/openapi.json`) serves OpenAPI 3.1. `/docs` serves an
offline-capable explorer using only assets embedded in this gateway. The console
links to it under Gateway → API reference. Paths come from the registered router,
excluding opaque proxy wildcards. Inference request schemas describe chat,
embeddings and Responses; remaining control-plane schemas are explicitly
permissive route inventories, not complete field-level contracts. API reference
versioning does not promise compatibility for experimental endpoints.

The explorer sends nothing until requested. Non-GET calls require confirmation;
bearer keys stay only in page memory. Requests reject cross-origin targets and
redirects, have a 90-second cap and a 1-MiB display cap, and can be cancelled.
Multipart file uploads use an SDK/CLI. Public docs contain no deployment config,
credentials or user data; every underlying endpoint retains its authorization.

Pinned official SDK contract checks: Python `openai==3.8.0`, Node `openai@7.10.0`.
Both exercise models, chat JSON/SSE, token-array embeddings, native Responses
JSON/SSE, owned multi-turn, retrieval, input-items and deletion; Python also checks
`store=false` and validates the served spec with `openapi-spec-validator==0.9.0`.
Fixtures are local Go-owned servers, not live providers. Advanced SDK convenience
accumulators and every possible event/tool schema are not qualified by this suite.

Reproduce with disposable Postgres/Redis, Node and `uv` installed:

```sh
npm ci --prefix tests/sdk
GATEMUX_SDK_TEST=1 \
TEST_DATABASE_URL='postgres://gatemux:gatemux@127.0.0.1:55434/gatemux?sslmode=disable' \
GATEMUX_TEST_REDIS_ADDR=127.0.0.1:56380 \
go test ./internal/api -run TestOfficialOpenAISDKContracts -count=1 -v
```

## Native Responses

- `POST /v1/responses`: inline text/items, client-executed function/custom tools,
  text-format/reasoning controls, JSON and native named SSE events.
- `GET /v1/responses/{responseID}`: retrieve owned upstream state as JSON.
- `GET /v1/responses/{responseID}/input_items`: bounded pagination with
  `after`, `before`, `order` and `limit` (1–100).
- `DELETE /v1/responses/{responseID}`: delete upstream state then gateway binding.
- `previous_response_id`: owned, completed/incomplete state on the same alias,
  principal, customer attribution and original deployment.

Requests retain unknown JSON fields and exact numbers. Only routing model,
previous ID and an omitted output-token cap are changed on the upstream wire.
The gateway default is **1024 max_output_tokens**; explicit values must be 1–1M.
Input/output JSON is capped at 8 MiB and each SSE event at 1 MiB. Deployment
first-event, idle, total and write policies also apply. Chat `[DONE]` is never
invented for Responses; malformed, truncated, mismatched and failed native
events are not reported as successful terminal events.

Native routing defaults to enabled for `openai`/`openai_compatible` adapter
families. `supports_responses` in deployment create/update (and its UI checkbox)
can disable it or explicitly enable it for a compatible upstream. This flag does
not translate another provider's protocol; an adapter must implement the native
interface and the deployment must actually offer `/responses`. Embedding-only
deployment overrides do not implicitly acquire Responses capability. Azure,
Bedrock, Gemini, Vertex and other cloud-native Responses parity is not claimed.

Creation uses model allowlists, principal/model/customer/provider concurrency,
distributed key/team rate checks and existing budget reservations. Actual native
input/output and cache/reasoning details settle via the shared price engine.
Missing/invalid authoritative usage conservatively settles the estimate. An
accepted HTTP response is never replayed after a body/stream failure. Retrieval
and deletion require authorization and concurrency/RPM admission, but do not
create a new inference charge.

## Ownership and retention

Migration 0036 stores **metadata only**: a random `resp_gatemux_…` ID mapped to
team, principal, customer, alias, deployment fingerprint, upstream ID, status and
token totals. Provider content stays upstream (independent request-capture policy
still applies elsewhere). Native IDs are never accepted as gateway lookup IDs.
Unknown, expired, cross-team and cross-key access returns 404 before upstream IO.

Virtual keys are separate owners even inside one team. Rotated keys do not
inherit old response access. JWT principals use a subject-derived digest within
their team; synthetic JWT principals do not write invalid key foreign keys to
usage logs. Revocation and current model permissions continue to apply.

Stored state cannot fail over between deployments. Removing the original alias
target or changing its model, URL, provider, credential reference or region makes
it unavailable (503). Operators must preserve upstream account ownership when
rotating values behind an existing credential reference; secret values are not
stored in this metadata fingerprint.

Bindings expire after 30 days, are denied immediately on expiry and are pruned
by one bounded worker (up to 1000 rows per second, 2-second DB timeout). This
deletes gateway metadata, **not provider content**; provider retention policies
are independent. Upstream state may disappear sooner. Explicit deletion is the
supported upstream deletion operation. `store=false` forwards the provider flag
and creates no binding; its returned ID cannot be retrieved or used as history.

Rejected before upstream IO: background jobs, Conversations/saved-prompt
references, hosted tools (web/file search, code interpreter, etc.), provider file/
vector-store/container references, and item references. Their resource ownership
and non-token accounting need separate implementations. Stored stream resumption
is rejected; create a new stream or retrieve JSON. Inline tool results remain
supported. Customer budget/rate controls and runtime guardrails are tracked in
the next milestones, not implied by the route's existence.

Protocol references: [OpenAI Responses create](https://developers.openai.com/api/reference/typescript/resources/responses/methods/create),
[streaming events](https://developers.openai.com/api/docs/guides/streaming-responses),
[retrieve](https://developers.openai.com/api/reference/typescript/resources/responses/methods/retrieve)
and [delete](https://developers.openai.com/api/reference/python/resources/responses/methods/delete).
