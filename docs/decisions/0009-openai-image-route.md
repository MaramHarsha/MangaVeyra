# ADR 0009: OpenAI image route and OpenRouter image fallback

- Status: Accepted for Phase 1 — basic direct OpenAI generation verified; remaining live matrix pending (below)
- Date: 2026-11-07
- Amends: ADR 0007 (frozen image routes and fallback). Related: ADR 0003, ADR 0006.
- Boundary: `internal/ai` (`image_routes.go`, `openai_image.go`, `image.go`, `image_remote.go`), prompts in
  `internal/gen/image_prompt.go`

## Context

ADR 0006 added a direct OpenAI connection for script text, but every image job still used the placeholder because
ADR 0007 froze the hosted image routes to OpenRouter Seedream 4.5 and fal. The owner wants GPT Image art from the
same OpenAI connection, with OpenRouter models selectable as backups. The facts below are frozen from official
documentation on 2026-11-07. OpenAI recommends the GPT Image 2.5 models for new integrations; transparency and the
xhigh/max quality levels are documented for 2.5 only, so moving to 2.6 needs a separate decision.

## Fixed remote routes (added to ADR 0007's table)

| Provider | Endpoint and model | Request ceiling | Per-call reservation |
|---|---|---|---|
| OpenAI (direct; Auto prefers it) | `POST https://api.openai.com/v1/images/generations`, or `POST https://api.openai.com/v1/images/edits` when input images are sent (JSON `images[]`/`mask` data URLs); `gpt-image-2.5-sunburst`, `gpt-image-2.5-flare` | 1–4 outputs; `quality: high`; PNG; `moderation: auto`; `background` `opaque`/`transparent`; one of 15 frozen sizes (1024², 1120×896, 1152×864, 1536×1024, 1536×864, 1536×768, 1536×640, 1536×512 and their transposes) chosen by nearest aspect ratio; composed prompt ≤ 6,144 UTF-8 bytes; ≤ 2 reference images (an identity contact sheet counts as one) re-encoded at ≤ 1,024 px, plus init and mask | `T(size) × n × $30/M + 6,144 × $5/M + inputs × 6,800 × $8/M`, where inputs = min(refs, 2) + init + mask |
| OpenRouter (pin-only) | `POST https://openrouter.ai/api/v1/images`; `openai/gpt-image-2.5-sunburst` | 1–4 outputs; `quality: high`; PNG; `background`; a documented `aspect_ratio`; ≤ 2 refs; init and mask ≤ 1,536 px; `provider.allow_fallbacks: false` | `5,930 × n × $30/M + 6,144 × $5/M + inputs × 6,800 × $8/M` |
| OpenRouter (pin-only) | same endpoint; `google/gemini-nano-banana-2.1` | exactly 1 output; `resolution: 1K`; a documented `aspect_ratio`; ≤ 6 refs plus init and mask | `1,120 × $30/M + 4,096 × $7.50/M + 6,144 × $1.50/M + inputs × 1,120 × $1.50/M` |
| OpenRouter (pin-only) | same endpoint; `google/gemini-3-pro-image` | as above | `1,120 × $120/M + 4,096 × $12/M + 6,144 × $2/M + inputs × 560 × $2/M` |

T is OpenAI's published GPT Image 2.5 output-token rule from the image generation guide's cost calculator: at high
quality, d = round-half-even(48 / (long/short)) and T = ceil(48 × d × (2,000,000 + W × H) / 4,000,000), so 1024² is
1,756 tokens. Examples: 1024² with no inputs reserves USD 0.0834; 1536×1024 USD 0.07188; 1536×512 USD 0.04677; two
references, or an init plus mask, add USD 0.1088; four 1024² outputs reserve USD 0.24144. The value 5,930 is T at the
largest documented pixel budget (2,880²), because OpenRouter derives the pixel size from the aspect ratio.

Two figures are application allowances, not published ceilings. OpenAI has not published the 2.5 input-image rule;
6,800 tokens per input image exceeds the largest published GPT Image high-fidelity figure for an input up to 3:1
(6,692). The 4,096 Gemini text/reasoning tokens are likewise an application allowance. All sums are exact integer
nano-dollars rounded up to whole microdollars.

ADR 0007's Seedream and fal rows are unchanged, and Seedream stays the OpenRouter route Auto chooses. Only the routes
in ADR 0007 and this ADR can dispatch; manifests cannot add routes, endpoints or tariffs.

## Dispatch and accounting

An enabled OpenAI provider row (ADR 0006) can serve images without re-entering its key, and the key is opened only
at dispatch. Route ceilings (prompt bytes, output counts) are checked before reserving. Each call has one ADR 0003
reservation, and the dispatch guard runs again after reserving.

Direct OpenAI actual cost comes from reported usage: text input × $5/M + image input × $8/M + all output tokens ×
$30/M; if the text/image split is missing, every input token is priced at $8/M. The amount is rounded up and not
clamped, so an actual above the estimate is recorded and blocks later reservations (ADR 0003). Missing usage and
unknown outcomes charge the estimate. HTTP rejections reconcile at zero (ADR 0006), except `moderation_blocked` at the
output or an unknown stage, which charges the estimate. OpenRouter routes keep ADR 0007's usage.cost and failure
accounting.

Errors map to safe codes: a 403 whose error has type `invalid_request_error` and no code (OpenAI's
organisation-verification refusal, detected from status, type and code only) → `provider_verification`, terminal and
unbilled, with the copy "OpenAI needs your organisation verified for image generation. Verify it in your OpenAI
account, then test the connection again."; other 401/403 responses and `insufficient_quota` → provider_auth; 429 →
rate_limited (retryable);
404/`model_not_found` → no_provider; `moderation_blocked` → provider_refused; other image user errors → bad_input;
5xx and timeouts → retryable. Provider text, keys and prompts never appear in errors or logs. A
`provider_verification` refusal is remembered on the connection for the refused key: routing skips its GPT Image
routes and G2 shows "Verify org" until the next connection test, and queued plans for those routes fail before
reserving.

Output must be base64; URLs are refused, and the direct route requires PNG. Each image is limited to 24 MiB, 4,096 px
per side and 16 MP. Redirects are disabled and each call has a 3-minute deadline. Generated images are centre-cropped
into the layer.

For edits, the init is letterboxed into the frozen size without cropping. The server mask (white = change) becomes an
alpha mask (alpha 0 = change), and the letterbox padding is protected. The result is cropped back, and the server
still restores every pixel outside the mask. OpenRouter routes send the init and mask as references with an edit
instruction. Character and effects layers ask for a transparent background; backgrounds, sheets and covers ask for
an opaque one. Only GPT Image routes honour transparency.

## Routing and fallback

In balanced, best and fastest modes, Auto ranks connected direct OpenAI image routes ahead of every other candidate:
Sunburst for balanced and best, Flare for fastest. free_only and local_only never consider them, and local_only
remains placeholder-only.

The three OpenRouter backups are pin-only: a project, character, panel-type or server model pin selects them, and
they never raise an unpinned estimate.

Automatic fallback skips the failed provider kind entirely: a transient OpenAI failure moves to the best OpenRouter
(or other) route, then to the placeholder. The plan's per-attempt estimate is the most expensive route in that chain,
with at most one route per approved attempt, and each reroute is checked against its own pinned quote. Pinned plans
and terminal failures (refusal, auth, organisation verification, bad input, spend limit) never fall back. Auto and
pins consider a direct OpenAI image model only if the connection's last model-list check listed it; a connection that
was never checked is unchanged. A skipped model is recorded in `skipped` as `<model>: not available for this key` (or
`needs organisation verification`), and routing continues in this ADR's order: another listed GPT Image model, the
other routes, then the placeholder. A plan approved before such a check is refused at dispatch, before reserving.

Each plan decision records the route, its per-call estimate and the fallback chain. A fallback decision also records
the failed `kind:model` and its error code, and the job event records the switch. The licence guard, monthly caps and
token approval thresholds apply unchanged. G11 Recent decisions stores, for each attempt, the per-call estimate of a
remote route and any switch (the failed `kind:model` and its safe code).

## Prompts

`internal/gen` builds deterministic, original prompts from the project style; the script panel (action, mood, camera,
shot) and the scene's setting and time of day; the character bible (name, look, default outfit); and a layer
instruction (a background plate with no people, one character on transparency in a full pose, or effects only). A
prompt written by a creator or an agent stays the primary description. Every prompt is at most 4,000 bytes, strips
control characters, mentions speed lines only when the panel has motion, and ends with fixed rules: no text, speech
bubbles, lettering or panel borders, and nothing explicit. Each layer has its own avoid list. Character layers carry
up to four kept references first and the pose guide last. When identity references and a control image (a pose
skeleton or a neighbouring page) exceed a route's MaxRefs, the identity references are packed into one contact sheet:
ceil(√n) columns in reading order, each reference fitted into a square cell on white with 16 px gutters and no labels,
at most 1,024 px (1024×504 for two references, 1024² for three or four). A GPT Image call therefore carries [identity
sheet, pose]. Without a control image the first MaxRefs identity references are sent as before. The GPT Image prompt
lists the attached images in sending order and gives guidance only for what was sent; Seedream names the pose guide by
its position. Quotes are unchanged. Backgrounds and effects carry no references.

## Live verification checklist (owner's key, USD 10 cap)

Basic direct-route evidence is recorded in [the 2026-11-07 live QA report](../qa/openai-live-2026-11-07.md).
The owner's saved OpenAI connection produced a background, transparent character and transparent FX through normal
jobs, all with `gpt-image-2.5-sunburst`. The composed page retains structured lettering. All three first-attempt
reservations reconciled, totalling USD 0.129615 from reported usage. These were portrait jobs without references;
they do not satisfy the square-image token assertion or the reference/mask/multi-output/backup checks below.

1. Connection test, then one 1024² background with n = 1: `usage.output_tokens` = 1,756 and actual ≤ USD 0.0834. A 403
   means the OpenAI organisation needs verification for GPT Image.
2. Character layer with two kept sheets: the PNG has real alpha; input image tokens per reference ≤ 6,800 (otherwise
   raise `gptImageInputTokens`); text output tokens ≈ 0.
3. Region edit: the change lands inside the white mask (alpha 0 = edit), output has the frozen size, record init and
   mask input tokens.
4. n = 2 bills the input once. 5. Latency at high quality with references stays under 3 minutes. 6. A fight scene passes
   `moderation: auto`; record any `moderation_stage`. 7. Pinned Nano Banana 2.1: `usage.cost` ≤ USD 0.0735 and its
   reasoning tokens. 8. Pinned OpenRouter GPT Image: actual pixel size and cost ≤ USD 0.209. 9. Seedream unchanged.

## Evidence (accessed 2026-11-07)

- OpenAI models and GPT Image pages: <https://developers.openai.com/api/docs/models>,
  <https://developers.openai.com/api/docs/models/gpt-image-2.6-sunburst>
- Image generation guide (models, sizes, quality, transparency, masks, moderation, cost calculator):
  <https://developers.openai.com/api/docs/guides/image-generation>
- Tariffs: <https://developers.openai.com/api/docs/pricing>
- Image API reference: <https://developers.openai.com/api/reference/resources/images/methods/generate>,
  <https://developers.openai.com/api/reference/resources/images/methods/edit>
- GPT Image input tokens (2.5 rule unpublished): <https://developers.openai.com/api/docs/guides/images-vision>
- OpenRouter Image API: <https://openrouter.ai/docs/guides/overview/multimodal/image-generation>; public catalogues
  <https://openrouter.ai/api/v1/images/models> and `/api/v1/images/models/{id}/endpoints`
- Google image tariffs and tokens per image: <https://aistudio.google.com/docs/pricing>

## Consequences

- Auto now spends on OpenAI images whenever an OpenAI connection is enabled; free_only and local_only keep the
  placeholder.
- Input-image and Gemini reasoning tokens rely on allowances, so a reservation is a true upper bound only once the
  live check confirms them.
- Neither OpenAI nor the OpenRouter Image API offers a request-level price guard; a tariff increase requires updating
  this record and the constants in `internal/ai/image_routes.go`.
- Seedream and Nano Banana return opaque character layers.
- The OpenAI connection test reports Text and Images separately from the model list (ADR 0006). An organisation not
  verified for GPT Image still lists the models, so the test cannot detect it; the first image job fails with
  `provider_verification` and is remembered as above.
