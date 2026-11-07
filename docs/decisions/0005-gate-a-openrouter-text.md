# ADR 0005: Gate A OpenRouter text boundary

- Status: Accepted for milestone 0001
- Date: 2026-11-07
- Related plan: `docs/plans/0001-script-vertical-slice.md`

## Context

Gate A needs one reproducible remote text path before the broader Phase 1 model
catalogue and fallback router exist. A hard pre-call spend cap also requires an
upper-bound estimate, not a best-effort token estimate. Provider pricing, context
limits and request features are external facts, so they are frozen here from the
current official OpenRouter documentation rather than inferred from old planning
notes.

> **Amended by ADR 0006 (2026-11-07):** a direct OpenAI route was added beside OpenRouter. Both routes now send a
> strict-mode-compatible schema: no `minLength`/`maxLength`, and `const` as a one-value `enum`. The server still
> validates every bound. `openai/gpt-4.1-mini` is marked deprecated upstream, so replacing it needs a fresh decision.

## Decision

Gate A supports exactly two text choices:

- the built-in deterministic `placeholder` provider with model
  `placeholder-text`; and
- an enabled OpenRouter connection using model `openai/gpt-4.1-mini` through the
  fixed `https://openrouter.ai/api/v1/chat/completions` endpoint.

Custom base URLs, other remote models, application-level failover and
OpenAI-compatible local servers are deferred to Gate B. OpenRouter may perform
its own provider routing inside one request; MangaVeyra still treats that as one
reserved outbound call.

The frozen model envelope is:

| Property | Gate A value |
|---|---:|
| Context window | 1,047,576 tokens |
| MangaVeyra maximum completion | 16,384 tokens |
| Maximum prompt price | $0.40 / 1M tokens |
| Maximum completion price | $1.60 / 1M tokens |
| Per-call reserved upper bound | $0.445245 |
| Per-attempt text-plan upper bound (initial + one repair) | $0.890490 |
| Queued action/job upper bound (3 attempts) | $2.671470 |

The reservation is deliberately conservative:

`ceil_microUSD((1,047,576 × 0.40 + 16,384 × 1.60) / 1,000,000)`.

It reserves a full context window at the prompt ceiling plus the complete output
allowance. This remains an upper bound even though input and output normally
share the context window. The request also sends
`provider.max_price = {prompt: 0.40, completion: 1.60}`; if OpenRouter cannot
route within those ceilings, the request fails before inference. A price change
therefore cannot silently exceed the amount reserved by MangaVeyra.

The request uses:

- `response_format.type = "json_schema"`, a task-specific schema and
  `strict = true`;
- `provider.require_parameters = true` so only providers supporting the request
  parameters are eligible;
- `usage.include = true` and the response's `usage.cost` for reconciliation;
  when cost is absent or invalid, reconciliation charges the reserved estimate;
- the fixed completion ceiling above; and
- no prompt, response body, authorization header or decrypted key in logs or
  persisted errors.

Before selecting OpenRouter, the adapter builds the exact system instruction,
user content and JSON schema that would be sent. It rejects the remote candidate
when their UTF-8 byte count plus a 4,096-token chat-envelope allowance and the
completion allowance exceeds the frozen context window. UTF-8 byte count is a
conservative upper bound for a byte-tokenized prompt. A valid stored 1 MiB source
that does not fit this bound may still use the placeholder; an explicit
OpenRouter pin returns `bad_input` instead of truncating private source text.

Every initial call and the single allowed repair call has its own durable spend
reservation. There is no unreserved retry. A repair repeats every dispatch guard,
uses the same schema and model, and reconciles independently. `TextPlan.Estimate`
advertises both possible calls for one claimed attempt; each call reserves only
its per-call bound. Generation multiplies that per-attempt plan by the job's three
allowed attempts before enqueue, so the durable job estimate and bearer-token
approval check cover the complete automatic-retry action.

`TextPlan.RoutingMode` is a security-relevant field. Planning validates and
stores the effective mode, and generation rechecks both the current instance
setting and this field immediately before each outbound call. The explanatory
`Decision` JSON is never trusted for enforcement. `local_only` is checked during
planning, initial dispatch, repair dispatch and provider connection testing.

OpenRouter connection testing calls the authenticated, non-generation endpoint
`GET https://openrouter.ai/api/v1/key`. It does not send user content and does not
make a completion request. Stored `last_test` data is limited to sanitized state,
time and request ID.

## Provider secret lifecycle

The service allocates a provider ID before sealing the key. Additional
authenticated data is exactly `provider:<provider-id>:key`. Provider ID, scope
and owner are immutable; server scope has no owner and user scope requires one.
A blank key on ordinary update retains the existing ciphertext. Explicit
replacement reseals with a fresh nonce and recomputes the hint from the submitted
plaintext. Disable retains ciphertext; delete removes the row. DTOs never expose
`key_enc`, decrypted material, or a recoverable key fragment.

## Evidence

- OpenRouter model limits, price and structured-output support:
  <https://openrouter.ai/openai/gpt-4.1-mini>
- Structured outputs and `require_parameters`:
  <https://openrouter.ai/docs/guides/features/structured-outputs>
- Provider routing and `max_price`:
  <https://openrouter.ai/docs/guides/routing/provider-selection>
- Chat completions and usage accounting:
  <https://openrouter.ai/docs/api/api-reference/chat/create-a-chat-completion>
- Authenticated current-key inspection:
  <https://openrouter.ai/docs/api/api-reference/api-keys/get-current-api-key>

## Consequences

- Gate A behavior and tests are deterministic even if the wider catalogue
  changes later.
- The compute meter shows a safe maximum, not an optimistic prediction; actual
  cost is reconciled after the call.
- Some very large adaptation inputs use the offline placeholder unless a later
  gate adds chunking or a larger validated context path.
- Broader routing, local OpenAI-compatible endpoints, alternate remote models
  and application-level fallback remain Gate B work.
