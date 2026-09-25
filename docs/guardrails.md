# Enforced text guardrails (Beta)

This is the pilot safety contract, not a general content-safety guarantee.
`banned_terms` matches literal strings using Unicode simple case folding. It is
not regex configuration, PII detection, moderation, jailbreak detection, or an
inspection of encoded content. The old unused webhook/chain implementation was
removed; unsupported legacy specs fail closed until replaced by an administrator.

## Configure and assign

Open **Safety & limits → Guardrails**, choose a team slug or model alias, then
Load policies. Add/edit/remove draft rules and Save policies after confirmation.
Each rule is assigned to exactly that scope. Team and model rules are additive:
a model rule cannot disable, replace, or weaken a team rule of the same name.

Only administrators can read definitions or change assignments:

- `GET /admin/guardrails`: first 500 assigned rules and scoped 24-hour hit/block
  counts. For more rules, load a scope directly. This is not an exhaustive export.
- `GET /admin/guardrails/{scope}/{subject}`: current JSON array; scope is `team`
  or `alias`, and subject is the URL-encoded team slug or model alias.
- `PUT /admin/guardrails/{scope}/{subject}`: `{ "expected": <old array>,
  "policies": <new array> }`. A missing/deleted scope returns 404; an intervening
  edit returns 409. The assignment and content-free administrative audit commit
  in one transaction. An empty new array removes protection from that scope.

Example rule:

```json
{"name":"internal-terms","type":"banned_terms","mode":"redact",
 "phase":"both","terms":["internal secret","example credential"]}
```

Modes: `block` rejects matched content; `redact` replaces the union of matched
spans with `[REDACTED]`; `flag` records matches without changing content. Phases:
`pre` (request), `post` (response, including streams), or `both`. Validation rejects
unknown options, unknown types, invalid phases/modes and duplicate scope names.
Removing/editing a policy affects subsequent requests, not an already-running
stream. Policies are read afresh before inference; there is currently one extra
bounded Postgres policy read per parsed request, not a hot-path policy cache.

## Supported protected traffic

`POST /v1/chat/completions` with plain string content, system/developer/user/
assistant roles and one choice. Only the OpenAI-compatible adapters named
`openai`, `openai_compatible`, `azure_openai`, `ollama` and `vllm` are qualified
for this filter path. This does not certify the cloud adapters themselves.

Recognized request fields are model, messages, stream/stream_options, temperature,
max_tokens/max_completion_tokens, top_p, stop, n=1, presence/frequency penalties,
seed and user. Inspection is of each message's content, not model names, IDs,
sampling controls, usage metadata or customer identity. Terms are matched within
individual messages, not by concatenating separate messages.

Tools/functions, tool results, multimodal parts, reasoning/refusal content,
logprobs, unknown extensions and translated providers are explicitly unsupported
when protection is assigned. Native Responses/resources, embeddings, Messages,
images, audio and passthrough cannot bypass assigned policies. Opaque passthrough
is also rejected if *any* model guardrail exists, since it has no trustworthy
model identity. Unprotected traffic retains its existing compatibility contract.

Protected requests bypass the response cache, so old entries cannot bypass a new
policy. Redaction changes the actual JSON request/response envelope; captured
successful payloads contain redacted content. Blocked output is not captured.
Original usage remains billable even when output is withheld.

## Bounds and failure behavior

- Eight policies per scope, 32 terms per policy, 128 UTF-8 bytes per term,
  64 KiB serialized scope configuration; request/response inspection cap 2 MiB.
- Provider SSE parser retains its 1 MiB event cap. The filter buffers only the
  maximum term's suffix, at most 1,024 bytes when overlapping redactions require
  extra retention; pathological overlap fails closed. Output expansion is capped.
  It never buffers the whole response. Delivery may lag by up to 127 characters.
- A blocked stream emits a normalized error and no `[DONE]`; already-delivered
  safe prefixes cannot be retracted. Missing finish/terminal events fail closed.
  Provider comments/event IDs/error bodies are not forwarded on protected streams.
- Policy and audit operations have two-second deadlines; unknown/invalid policies,
  unavailable audit storage, malformed data and unsupported output fail closed.
  There is no fail-open fallback and no external safety-service call in this slice.
- Audit aggregates at most one result per assigned policy per phase (plus a
  classified validation failure), not a row per chunk. `guardrail_decisions`
  stores correlation, team/model, phase, decision and inspection time, never
  matched text or terms. Audit is synchronous before non-stream output or the
  terminal stream marker. An outage may interrupt a partially delivered stream;
  it cannot guarantee an audit write to an unavailable database.
- Metrics: `gatemux_guardrail_decisions_total` and
  `gatemux_guardrail_duration_seconds` have fixed phase/outcome labels. Audit errors
  are observable even when Postgres cannot record a decision.

## Verification

Unit tests cover validation, all-message/raw-envelope redaction, every stream
split point (including Unicode simple folds), memory bounds and unsupported
shapes. Postgres-backed API tests cover role boundaries, compare-and-swap,
team isolation, additive model policy, denial, redaction, flags, one audit per
stream rule, settlement and permit release. A closed audit destination is tested
without stopping shared services. The actual-browser fixture performs create,
edit, removal and team/model assignment against its isolated schema; no mocked
admin responses or paid providers. See [contributing](../CONTRIBUTING.md) for tests.
