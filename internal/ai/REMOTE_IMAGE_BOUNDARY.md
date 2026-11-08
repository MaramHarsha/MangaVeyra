# Phase 1 image adapter boundary

Implementation evidence for the root-owned Phase 1 ADR; checked 2026-11-07.
This does not amend ADR 0005's text model or failed-call accounting.

| Adapter | Fixed endpoint/model | Application ceiling | Reserved per outbound call |
|---|---|---|---|
| OpenRouter | `POST https://openrouter.ai/api/v1/images`, `bytedance-seed/seedream-4.5` | `n` 1–4; 1K/2K/4K; at most 14 total image inputs including init and mask; PNG, no streaming or provider fallback | `$0.04 × n` |
| fal generation | `POST https://fal.run/fal-ai/flux/schnell` | explicit width/height ≤1024; 1–4 outputs; four steps; safety enabled; no extensions | `$0.003 × n × ceil(width × height / 1,000,000)` |
| fal inpainting | `POST https://fal.run/fal-ai/flux-lora/inpainting` | same dimensions/count; init+mask; 28 steps; empty LoRA list; safety enabled | `$0.035 × n × ceil(width × height / 1,000,000)` |

ADR 0009 (proposed) adds the rows in `image_routes.go`: direct OpenAI GPT Image
2.5 Sunburst/Flare (`/v1/images/generations` and `/v1/images/edits`, Auto
preferred) and pin-only OpenRouter GPT Image 2.5 Sunburst, Nano Banana 2.1 and
Nano Banana Pro. Their quotes are integer nano-USD sums of the published
output-token rule, a 6,144-byte prompt bound and per-input-image allowances;
`openai_image_test.go` pins every figure.

1024² conservatively reserves two decimal megapixels per image: $0.006 for
Schnell and $0.07 for fal inpainting. Unknown routes or manifest-supplied prices
cannot dispatch. HTTP redirects are disabled, and fal output downloads allow
only HTTPS fal.media hosts with no Authorization header.

These bounds are valid at the frozen tariffs. Neither fal nor the documented
OpenRouter Image API provides a verified request-level price ceiling. The
Image API's documented provider fields omit `max_price`; the implementation
does not claim otherwise. Tariff increases require an ADR/constants update.

Seedream accepts the init image and binary edit mask as references with an edit
instruction. This is a server-constrained masked edit, not native provider
inpainting. The server copies every black-mask pixel from the original image
and returns PNG. Model output dimensions are bounded before decoding and fitted
to the requested dimensions. Seeds are reported as unknown (0) for this route.

Each billable HTTP call gets one durable reservation. Unknown outcomes and
success responses without valid usage charge its estimate. OpenRouter Image
API HTTP failures reconcile at zero under its all-or-nothing billing policy;
this policy is limited to image calls. fal definite rejection and server-error
responses reconcile at zero; unknown outcomes retain the estimate. Malformed
successful outputs still consume their reported or estimated generation cost.

`ImagePlan.Estimate` includes the largest eligible per-attempt route cost.
Generation must multiply it by its approved attempt count (at most three).
`ImageFallback` rejects old text jobs, child jobs, pins, terminal policy errors,
attempt exhaustion, cost increases and paid fallback from an initial local job.
Parent image jobs always use executor `go`; a local image plan creates one
worker child for its parent attempt with executor `worker` and one attempt.

Both instance and request `local_only` mean placeholder only. Custom text HTTP
and worker dispatch stay blocked until same-host locality can be proven.
`free_only` can use an explicitly connected worker; it never falls back to a
paid route. Local custom text HTTP can only connect to private/loopback IPs,
requires the admin's private-URL setting, and never redirects or uses a proxy.

Official sources:

- [OpenRouter Image API](https://openrouter.ai/docs/guides/overview/multimodal/image-generation)
- [Seedream 4.5 limits and tariff](https://openrouter.ai/bytedance-seed/seedream-4.5)
- [OpenRouter provider price fields](https://openrouter.ai/docs/guides/routing/provider-selection)
- [fal Schnell](https://fal.ai/models/fal-ai/flux/schnell)
- [fal inpainting](https://fal.ai/models/fal-ai/flux-lora/inpainting)
- [fal pricing policy](https://fal.ai/docs/documentation/model-apis/pricing)
- [fal synchronous API: no retries](https://fal.ai/docs/api-reference/inference-api/sync/run)
- [Ollama OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility)
- [Ollama cloud-disable setting](https://docs.ollama.com/faq)

## Phase 4 routes (ADR 0013 §9; fake-transport tested, not live-verified)

`image_routes.go` adds one row per frozen route; each adapter file cites the
provider documentation it follows (accessed 2026-11-09).

| Adapter | Endpoint and model | Ceiling | Reserved per call |
|---|---|---|---|
| Google | `POST …/v1beta/models/gemini-nano-banana-2.1:generateContent` (Imagen is shut down on the Gemini API) | one 1K image, `maxOutputTokens` 4,096, ≤ 6 references, no mask | `(1,120 + 4,096) × $30/M + 6,144 × $1.50/M + inputs × 1,120 × $1.50/M` |
| xAI | `POST https://api.x.ai/v1/images/generations`, `grok-imagine-image` | 1–4 outputs, `resolution: 1k`, no inputs | `$0.02 × n` |
| Together | `POST https://api.together.ai/v1/images/generations`, `black-forest-labs/FLUX.1.1-pro` | ≤ 1024 px, multiples of 8, 1–4 outputs | `$0.04 × n × ceil(MP)` |
| Replicate | `POST /v1/models/black-forest-labs/flux-schnell/predictions` (official, unpinnable) | 1–4 outputs, `megapixels: "1"` | `$0.003 × n` |
| Replicate (pin-only) | `POST /v1/predictions`, `stability-ai/sdxl:7762fd07…9bdc` | ≤ 1024 px, `Cancel-After: 120s` | `120 s × $0.000975/s` (application bound) |
| Runware | `POST https://api.runware.ai/v1`, `runware:100@1` | ≤ 1024 px, multiples of 64, 1–4 results | `$0.006 × n × MP` (application allowance) |

Reported costs (`usageMetadata`, `cost_in_usd_ticks`, `includeCost`,
`predict_time`) reconcile unclamped; HTTP rejections reconcile at zero except
xAI 400/5xx (xAI charges moderated generations). Replicate and fal-style CDN
downloads allow only the provider's HTTPS delivery hosts and carry no key.
Custom G12 image connections (ComfyUI, HTTP) are servers on this machine or
network: ADR 0007's custom text rules, no reservation; a Hugging Face image
endpoint reserves zero per call because it is billed per endpoint hour.
