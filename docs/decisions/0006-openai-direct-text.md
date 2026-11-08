# ADR 0006: OpenAI direct text route

- Status: Accepted
- Date: 2026-11-07
- Related: ADR 0003 (hard remote-spend reservations), ADR 0005 (Gate A OpenRouter text boundary),
  `docs/plans/0002-phase-1-first-playable.md`

## Context

ADR 0005 limited remote script generation to one frozen OpenRouter route and deferred direct vendor keys to Gate B.
Creators asked to connect an OpenAI key directly. A second remote route must keep every Gate A guarantee:
- a hard pre-call spend reservation that is a true upper bound;
- `local_only` and provider state rechecked before every outbound call;
- sealed keys that are never shown;
- no user-controlled endpoint;
- a key check that sends no user content.

The facts below are frozen from OpenAI's official documentation (see Evidence) on 2026-11-07. While checking them we
found two facts that also affect ADR 0005:
1. `gpt-4.1-mini` is now marked deprecated. That is the model ADR 0005 uses through OpenRouter.
2. Strict structured outputs return an error for schemas that use `minLength`/`maxLength`, and `const` is not in the
   supported list. The Gate A script schema used both.

## Decision

Add a second remote text route, `openai`, beside `openrouter`. Each route is one row in a frozen route table
(`internal/ai/routes.go`). Planning, dispatch, reservation and connection testing all read from that table, so a plan
can never pair a provider with another route's model or price.

| Property | OpenAI route |
|---|---:|
| Endpoint | `POST https://api.openai.com/v1/chat/completions` (fixed; no base URL) |
| Model | `gpt-6-luna` |
| Reasoning effort | `none` (reasoning tokens would be billed as output and share the completion budget) |
| Stored by OpenAI | `store: false` (prompts may contain private adaptation source) |
| Maximum input MangaVeyra sends | 272,000 tokens (below OpenAI's long-context price tier) |
| Maximum completion | 16,384 tokens (`max_completion_tokens`) |
| Prompt price | $0.10 / 1M tokens |
| Completion price | $0.50 / 1M tokens |
| Per-call reserved upper bound | $0.035392 |
| Per-attempt plan bound (initial + one repair) | $0.070784 |
| Queued job bound (3 attempts) | $0.212352 |

The reservation is `ceil_microUSD((272,000 × 0.10 + 16,384 × 0.50) / 1,000,000)`. Before choosing this route, the
adapter builds the exact system instruction, user content and schema for both the initial and the repair request. It
counts their UTF-8 bytes (an upper bound on tokens) plus a 4,096-token envelope, and requires the total to stay within
272,000. A larger adaptation skips the OpenAI route: it uses another eligible connection or the placeholder, and a
pin to this provider returns `bad_input`. Private source is never truncated.

**Reconciliation.** OpenAI responses report token usage, not cost. Actual cost is reported prompt tokens × $0.10 plus
completion tokens × $0.50 per million, rounded up to a whole microdollar and clamped to the per-call reservation. Both
of the following still charge the reserved estimate, as in ADR 0005:
- a request whose outcome is unknown (transport error after sending);
- a success response without usage.

An HTTP error response is a definite rejection that OpenAI does not bill, so it reconciles at 0. This differs from
the OpenRouter route, which still follows ADR 0005.

OpenRouter's request-level `max_price` has no OpenAI equivalent. Prices are frozen here: if OpenAI raises them, the
reservation may no longer be an upper bound until this record and the constants in `internal/ai/openai.go` are
updated.

**Routing.** Candidates are the enabled connections of both kinds: personal before server, oldest first. The router
takes the first candidate whose route can hold the input and that matches any model pin. A provider pin must match
its route's model. Dispatch re-resolves the provider and refuses a kind/model mismatch before reserving or sending.

**Connection check.** `GET https://api.openai.com/v1/models`. It is authenticated, generates nothing and sends no
user content. One response reports the connection's two capabilities: Text (`gpt-6-luna` is listed) and Images (each
ADR 0009 GPT Image route model, `gpt-image-2.5-sunburst` and `gpt-image-2.5-flare`, is listed). Both are stored with
the check and shown in G2. The check passes when either capability is ready; a key that lists neither fails as
`no_provider` and both are shown as unavailable. 401/403 → `provider_auth`; `insufficient_quota` → `provider_auth`,
not retried. A failed HTTP check records no capability. Text routing skips a connection whose last check did not list
the text model, and a provider pin to it returns `no_provider`. A connection that was never checked, or whose key was
replaced, routes as before.

**Strict schema (both routes).** Requests send `strictScriptSchema()`. It is the script schema with
`minLength`/`maxLength` removed and `const` written as a one-value `enum`. `decodeScriptDocument` still enforces
every length, value and structure rule on the response, so validation is unchanged. This also fixes the OpenRouter
route, which forwards `response_format` to OpenAI with `require_parameters`.

**Character allow-list in the schema (both routes).** The schema is built per request:
- `dialogue.character_id` is an `enum` of `""` plus the request's allowed character IDs;
- `character_ids` is an array of those IDs (`maxItems: 0` when the project has none).

The first live run showed why. For a project without characters, the model invented IDs in both the initial and the
repair response, and both failed validation. The worst case (256 characters) stays inside strict mode's limits of
1,000 enum values and 15,000 characters per enum, and a test checks it.

**Repair names the failed check.** Decode failures carry a fixed validation label, for example `duplicate panel
key`. The label is a literal in the decoder and never contains provider output. It is appended to the single repair
instruction (bounded to 128 bytes and counted in the context-fit check) and shown in the job's error message. The
instructions also state the rules a schema cannot express: unique keys across the document, non-decreasing page
numbers, and non-empty panels.

Secret handling is unchanged from ADR 0005 (AAD `provider:<id>:key`, no key in views, logs or errors).

## Evidence

- Model limits, pricing and feature support: <https://developers.openai.com/api/docs/models/gpt-6-luna>
- Model catalogue and deprecation markers: <https://developers.openai.com/api/docs/models>,
  <https://developers.openai.com/api/docs/models/gpt-4.1-mini>
- Supported schemas in strict mode: <https://developers.openai.com/api/docs/guides/structured-outputs#supported-schemas>
- Chat Completions request and usage fields:
  <https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create>

## Consequences

- Creators can generate scripts with a direct OpenAI key. A call costs about 12× less than the OpenRouter bound
  ($0.035 vs $0.445), and the spend cap is consumed accordingly.
- Very large adaptations need OpenRouter (1M context) or the placeholder until chunking exists.
- ADR 0005's model (`openai/gpt-4.1-mini`) is deprecated upstream. Replacing it is a separate decision and needs
  fresh OpenRouter facts.
- **Live verification (2026-11-07, user's key):**
  - `Test connection` returned OK, confirming the key can use `gpt-6-luna`.
  - First generation: failed validation twice (invented character IDs), cost $0.003265. This led to the allow-list
    schema above.
  - Second run, with the failing check shown in the error: same cause confirmed, cost $0.002609.
  - Third run, after the fix: saved on the first call in about 15 s for $0.000884 (reserved $0.035392). It produced
    3 scenes, 12 panels over the 6 requested pages, 10 dialogue lines and 12 SFX, and it is editable.
