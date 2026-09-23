# Worker protocol v1

AI workers are separate processes (the Python package in `worker/`, or anything else that speaks this protocol).
They **pull** jobs from a MangaVeyra server over HTTPS; the server never connects to a worker. One server can have
many workers (a local GPU box, a rented GPU, a laptop). This document is licensed MIT (see `sdk/LICENSE`).

## Authentication

Every request carries `Authorization: Bearer mvw_<token>`. Worker tokens are created by an admin on **Models &
providers → Local worker → Add worker** (or the `MV_WORKER_TOKEN` shared secret for `docker compose` pairing).
Tokens are stored hashed on the server and can be revoked at any time.

All bodies are JSON (`Content-Type: application/json`) unless noted. Errors: `{"error":{"code":"...","message":"..."}}`.

| Status | Meaning | What a worker does |
|---|---|---|
| `401` | Token missing, wrong or revoked | Stop and tell the operator |
| `404` `worker_not_found` | The server no longer knows this `worker_id` (deleted by an admin, or a different token) | Register again, then continue |
| `409` `lease_lost` | On `/jobs/{id}/…`: the job is no longer leased to this worker (re-queued after missed heartbeats or an expired lease, canceled, deleted, or already completed by an earlier try of the same request) | Drop the job silently; do not call `fail` |
| `403`, `404`, `410` on `/jobs/{id}/…` | The job belongs to another worker or no longer exists | Same as `409 lease_lost` |
| `408`, `425`, `429`, `5xx`, network errors | Transient | Retry with exponential backoff and jitter (honour `Retry-After`); keep retrying `artifacts`, `complete` and `fail` for at least one lease so a finished job survives a server restart |

## Endpoints

### `POST /api/worker/v1/register`
```json
{
  "name": "studio-pc",
  "version": "1.0.0",
  "backends": ["diffusers", "comfyui", "mock"],
  "gpus": [{"name": "NVIDIA GeForce RTX 4090", "vram_total_mb": 24564, "vram_free_mb": 20110}],
  "job_types": ["image.generate", "image.edit", "image.inpaint", "character.score", "detect.panels"],
  "models": ["qwen-image-edit", "flux2-klein", "illustrious-xl"],
  "max_concurrent": 2
}
```
Response `200`: `{"worker_id": "01J…", "heartbeat_interval_s": 15, "lease_s": 120}`.
Re-registering with the same token and name returns the same `worker_id`.

### `POST /api/worker/v1/heartbeat`
`{"worker_id": "…", "gpus": [...], "running": ["job-id", …]}` → `{"cancel": ["job-id", …]}`.
Heartbeats also extend the leases of the listed jobs. `cancel` lists jobs the worker must stop: canceled ones and ids
in `running` that are not (or no longer) leased to it.
A worker that misses three heartbeats is marked offline and its running jobs return to the queue (or fail over to
another candidate through the router).

### `POST /api/worker/v1/claim`
`{"worker_id": "…", "job_types": [...], "models": [...], "wait_s": 20}`
Long-polls up to `wait_s` seconds (the server may cap it; the reference server caps at 30). `204 No Content` when
nothing is available. A server may answer `204` at once without waiting (for example while an admin drains the
worker), so clients must pace their claims (back off when a `204` arrives much sooner than `wait_s`) instead of
re-polling in a tight loop. `200`:
```json
{
  "job": {
    "id": "01J…", "type": "image.inpaint", "attempt": 1, "lease_s": 120,
    "model": {"id": "flux2-klein", "manifest": { "...": "model manifest, see internal/ai/catalog" }},
    "input": { "...": "type-specific, see below" },
    "files": {"image": "https://studio.local/media/…?sig=…", "mask": "https://…"}
  }
}
```
`files` are short-lived signed URLs for inputs (download with plain GET, no auth header; they may redirect to object
storage). `model.id` is `""` when the job has no specific model (model-agnostic types, or "any installed model"):
the worker picks one it serves. `attempt` counts from 1 and grows when the server retries the job.

### `POST /api/worker/v1/jobs/{id}/progress`
`{"progress": 0.62, "step": 18, "steps": 28, "message": "denoising", "preview_png_b64": "…optional ≤64 KB…"}`
→ `{"cancel": false}`. Progress also extends the lease; a worker that is busy without progress to report (loading a
model, uploading) resends its last progress at least every `lease_s / 3` seconds. If `cancel` is true the worker must
stop and call `fail` with code `canceled` (answered with `409 lease_lost` when the lease itself is gone, which the
worker then ignores). A preview that is not a PNG or exceeds 64 KB is dropped.

### `POST /api/worker/v1/jobs/{id}/artifacts` (multipart/form-data)
Fields: `name` (e.g. `variation-1`; 1–64 of `A–Z a–z 0–9 . _ -`, starting with a letter or digit), `meta` (JSON
string, optional) and `file` (binary, one per request). Send `name` and `meta` **before** `file`: the server streams
the file to storage as it arrives. The stored extension follows the sniffed content type, so the returned key is the
only reference to use. → `{"key": "jobs/01J…/variation-1.png"}`. Upload every output file before `complete`;
re-uploading the same `name` replaces the file.

### `POST /api/worker/v1/jobs/{id}/complete`
`{"output": { "...": "type-specific, referencing artifact keys" }, "actual": {"gpu_seconds": 38.4}}` → `{"ok": true}`

### `POST /api/worker/v1/jobs/{id}/fail`
`{"error": {"code": "oom|timeout|safety|bad_input|canceled|internal", "message": "…", "retryable": true}, "actual": {"gpu_seconds": 3.1}}`
→ `{"ok": true}`. `actual` is optional (GPU time the failed attempt used, for the compute meter). Send only the six
codes above (report anything else as `internal`). The server decides whether to retry (`retryable`), fall back to another model (never for
`bad_input` or `canceled`), or fail the job.

## Shutdown

On SIGTERM a worker stops claiming, lets running jobs finish for a drain period while it keeps heartbeating, then
stops the rest and reports them with `fail` code `internal`, `retryable: true` so the server re-queues them at once.

## Job types

Sizes are pixels. Seeds are integers (`null` = random). Prompts are already expanded by the server (character sheets,
style, camera and pose are folded in); workers do not need project context.

| type | input | files | output |
|---|---|---|---|
| `image.generate` | `prompt`, `negative`, `width`, `height`, `n` (variations, 1–4), `seed`, `steps`, `guidance`, `style` (`bw_screentone`, `flat_grey`, `colour`), `loras` `[{"key","weight"}]`, `pose` (OpenPose JSON, optional) | `ref_0…ref_n` reference images (optional), `pose_image` (optional) | `{"images": [{"key", "seed"}]}` |
| `image.edit` | `prompt`, `n`, `seed`, `strength` | `image`, refs | `{"images": [...]}` |
| `image.inpaint` | `prompt`, `n`, `seed`, `feather_px` | `image`, `mask` (white = change) | `{"images": [...]}` — the server copies pixels outside the mask back exactly, workers need not |
| `sketch.ink` | `prompt`, `strength` (0 = loose, 1 = exact), `finish` (`screentone`, `flat_grey`, `colour`) | `sketch` | `{"images": [...]}` |
| `character.sheet` | `prompt`, `n`, `seed`, `style` | refs (optional) | `{"images": [...]}` (front / side / face sheet) |
| `character.train` | `trigger`, `base_model`, `steps`, `rank` | `ref_0…ref_n` | `{"lora": {"key"}, "test_images": [...], "score": 0.91}` |
| `character.score` | `threshold` | `reference_0…`, `candidate_0…` | `{"scores": [0.96, 0.81, …]}` (cosine similarity of face/identity embeddings, 0–1) |
| `detect.panels` | `reading` (`rtl`, `ltr`, `vertical`) | `page` | `{"panels": [{"points": [[x,y],…], "order": 1}], "bubbles": [{"points", "text", "vertical": false}]}` |
| `ocr` | `lang` | `image` | `{"lines": [{"text", "box": [x,y,w,h]}]}` |
| `upscale` | `scale` (2 or 4) | `image` | `{"images": [...]}` |
| `video.animate` | `shots` `[{"layers":[…], "motion":"push-in|parallax|shake", "duration_s"}]`, `width`, `height`, `fps` | per-shot layer images | `{"video": {"key"}, "duration_s"}` |
| `voice.tts` | `text`, `voice` | — | `{"audio": {"key"}}` |

Unknown job types must not be claimed: the server only hands out types listed at registration.
