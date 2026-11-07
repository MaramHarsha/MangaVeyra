# ADR 0007: Frozen image routes, local text, and bounded worker orchestration

- Status: Accepted for Phase 1
- Date: 2026-11-07
- Boundary: `internal/ai`, consumed by lease-fenced `internal/gen` jobs
- Amended by: ADR 0009 (direct OpenAI GPT Image route preferred by Auto, pin-only OpenRouter image backups and the
  automatic image fallback chain)

## Fixed remote routes

Only the following hosted image routes can dispatch. Administrator manifests cannot add arbitrary remote endpoints,
tariffs, extensions, or models to these adapters. Provider keys stay sealed and are opened only at dispatch.

| Provider | Endpoint and model | Request ceiling | Per-call reservation |
|---|---|---|---|
| OpenRouter | `POST https://openrouter.ai/api/v1/images`; `bytedance-seed/seedream-4.5` | 1–4 outputs, documented 1K/2K/4K size tier, at most 14 input images including init/mask, PNG, no streaming or provider fallback | USD 0.04 × output count |
| fal generation | `POST https://fal.run/fal-ai/flux/schnell` | explicit dimensions ≤1024 per side, 1–4 outputs, four steps, safety enabled, no extensions | USD 0.003 × output count × ceil(width × height / 1,000,000) |
| fal inpainting | `POST https://fal.run/fal-ai/flux-lora/inpainting` | same dimensions/count, init and mask, 28 steps, empty LoRA list, safety enabled | USD 0.035 × output count × ceil(width × height / 1,000,000) |

The OpenRouter Image API is a dedicated API, rather than chat completions with an invented image payload. Its
documented provider fields do not establish a request-level `max_price` guard. fal's synchronous API is used without
automatic transport retries. These reservations are conservative application bounds at the frozen tariffs; neither
adapter claims protection against a later upstream tariff increase. Such an increase requires updating this ADR and
the tested adapter constants before enabling it. At 1024², the fal bound rounds up to two decimal megapixels: USD
0.006 for Schnell or USD 0.07 for inpainting per output.

Sources: [OpenRouter Image API](https://openrouter.ai/docs/guides/overview/multimodal/image-generation),
[Seedream 4.5](https://openrouter.ai/bytedance-seed/seedream-4.5),
[OpenRouter provider options](https://openrouter.ai/docs/guides/routing/provider-selection),
[fal Schnell](https://fal.ai/models/fal-ai/flux/schnell),
[fal inpainting](https://fal.ai/models/fal-ai/flux-lora/inpainting),
[fal pricing](https://fal.ai/docs/documentation/model-apis/pricing),
[fal synchronous API](https://fal.ai/docs/api-reference/inference-api/sync/run).

## Dispatch and accounting

Every billable call has one durable reservation bound to job, attempt, live provider/model, user and project. It
rechecks current policy before opening a key and sending data. Unknown outcomes and missing/invalid successful
usage consume the estimate. OpenRouter image HTTP failures reconcile at zero according to the Image API's
all-or-nothing billing contract. Definite fal rejection/server errors reconcile at zero; unknown outcomes retain
their reservation. Malformed successful output still consumes generation usage. These image policies do **not**
change ADR 0005's text failed-call accounting.

HTTP redirects are disabled. Provider output is size-, count-, MIME- and dimension-bounded before decoding.
Authenticated requests never follow returned output URLs; fal output downloads allow HTTPS fal.media hosts only
and carry no provider Authorization header. Images are fitted to the requested size. Seedream does not document a
reproducible output seed, so its reported seed remains unknown (0).

Seedream region edits send init/mask images as references with an edit instruction. This is reference-guided
editing. The server independently restores every pixel outside the white mask from the original; the same invariant
applies to native inpainting and the placeholder. Lettering never enters an art request or generated raster layer.

## Routing, licences and fallback

The catalogue records capabilities, supported providers, commercial permission, references, dimensions, quality,
speed and worker resource hints. Routing filters these before ordering balanced/best/fastest/free-only/local-only
choices. Public series reject models without commercial permission. Explicit provider/model/character/panel pins
are still subject to availability, capabilities, licences and instance policy. Explanatory JSON never grants access.

An `ImagePlan` retains the effective typed policy and the highest eligible per-attempt route estimate. The generation
owner reserves the aggregate number of independent layer calls and at most three parent attempts. Fallback is
limited to the new typed image parent jobs, transient failures, the approved aggregate estimate, and unpinned
requests. It never turns an initially local/free action into paid remote work, never reroutes text or worker children,
and never retries terminal policy/refusal errors forever. Every fallback revalidates the original policy and records
the durable route. No production acceptance test contacts a paid provider.

## Local text and worker images

Custom text implements an explicitly configured OpenAI-compatible local endpoint, including Ollama model tags.
It requires the administrator's private-provider-URL setting, restricts destinations to private/loopback addresses,
uses no proxy or redirects, caps tokens/output and makes no content-generating connection test. Endpoint names
alone are not proof of locality: Ollama may forward to cloud models. Accordingly both request and instance
`local_only` select the in-process placeholder and prohibit custom HTTP and worker handoff. Current settings are
read uncached at each dispatch/claim. The two decisions in ADR 0005 (unsent text call accounting and the deprecated
`openai/gpt-4.1-mini` model) remain open for the owner.

Sources: [Ollama OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility) and
[Ollama local/cloud configuration](https://docs.ollama.com/faq).

A non-local-only image plan may use an explicitly connected worker and an installed compatible manifest. Its Go
parent creates one child worker job per stable component slot in the current parent attempt, with bounded wait, artifact count/type/size
checks and cancellation on parent exit. The child has one attempt; the parent owns the retry envelope. Prompts and
references travel through the existing authenticated worker protocol and signed file URLs. App wiring supplies the
catalogue's `jobs.ManifestLookup` to claims. A failed or expired parent cannot persist late worker artifacts.
