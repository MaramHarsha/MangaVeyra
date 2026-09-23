# MangaVeyra worker

The AI worker for [MangaVeyra](https://github.com/MaramHarsha/MangaVeyra), the open-source, self-hosted AI manga
studio. A worker is a separate Python process that **pulls** jobs from your MangaVeyra server — image generation,
edits, inpainting, sketch inking, character sheets, character LoRA training, Character Lock scoring, panel
detection and OCR — runs them on local hardware, and streams progress back. The server never connects to a worker,
so a worker can run on the same machine, a desktop GPU on your LAN, or a rented GPU anywhere.

It speaks [worker protocol v1](../sdk/api/worker.md) (MIT). Licence: AGPL-3.0, like the rest of MangaVeyra.

Nothing leaves your machines: the worker only talks to your server (and, if you let it, downloads model weights from
Hugging Face). There is no telemetry.

## Contents

- [Install](#install) · [Configure](#configure) · [Backends](#backends) · [Adding a model](#adding-a-model)
- [Character LoRA training](#character-lora-training) · [ComfyUI workflows](#comfyui-workflows)
- [Vision helpers and OCR](#vision-helpers-and-ocr) · [GPU notes](#gpu-notes) · [Development](#development)

## Install

You need a **worker token** first: in MangaVeyra go to *Admin → Models & providers → Local worker → Add worker*
(or set the shared `MV_WORKER_TOKEN` in `docker compose`).

### Docker (recommended)

```sh
# From the repository root. GPU image: CUDA 12.8, PyTorch, diffusers, peft, ComfyUI client, OpenCV.
docker build -f deploy/Dockerfile.worker -t mangaveyra-worker:gpu .
docker run -d --name mv-worker --gpus all --restart unless-stopped --stop-timeout 150 \
  -e MV_SERVER=http://studio.local:8080 -e MV_WORKER_TOKEN=mvw_… -e MV_WORKER_NAME=studio-pc \
  -v mv-worker-models:/models mangaveyra-worker:gpu

# No GPU? The CPU image serves placeholder art (mock), real panel detection and a ComfyUI client.
docker build -f deploy/Dockerfile.worker --target cpu -t mangaveyra-worker:cpu .
```

Both images run as an unprivileged user (`worker`, uid 10002), keep weights, the LoRA cache and scratch space in
the `/models` volume, and have a `HEALTHCHECK` (healthy while the worker talked to the server in the last two minutes). Give the container a stop
timeout of at least `MV_DRAIN_TIMEOUT_S` (default 120 s) so running jobs can finish on `docker stop`.
`deploy/docker-compose.yml` starts them with `--profile gpu` or `--profile worker-cpu`; the compose names
`MV_SERVER_URL` and `MV_WORKER_BACKEND` are accepted as aliases of `MV_SERVER` and `MV_BACKENDS`.

### pip

```sh
python3.12 -m venv .venv && . .venv/bin/activate
pip install ./worker                              # mock backend only (Pillow, httpx) — works anywhere
pip install './worker[vision]'                    # + OpenCV panel/bubble detection, identity scoring
pip install './worker[diffusers,comfyui,vision]'  # everything (install a CUDA build of torch first, see GPU notes)

export MV_SERVER=http://studio.local:8080 MV_WORKER_TOKEN=mvw_…
mangaveyra-worker            # same as `mangaveyra-worker run`
```

### uv

```sh
uv venv --python 3.12 && uv pip install './worker[diffusers,comfyui,vision]'
uv run mangaveyra-worker
```

### Commands

| Command | What it does |
|---|---|
| `mangaveyra-worker run` (default) | Register, heartbeat, claim and run jobs until `SIGINT`/`SIGTERM`. |
| `mangaveyra-worker check` | Print detected GPUs, backends, job types and models as JSON. No server contact. |
| `mangaveyra-worker job TYPE --input JSON --file NAME=PATH --model ID --out DIR` | Run one job locally without a server — handy to test a model manifest or a ComfyUI bundle. |

Exit codes: `0` clean stop, `1` registration failed, `2` configuration error or the token was rejected.

## Configure

Every option is an environment variable; a TOML file (`--config worker.toml` or `MV_WORKER_CONFIG`, see
[`examples/worker.toml`](examples/worker.toml)) works too. Precedence: flags → environment → file → defaults.

| Variable | Default | Meaning |
|---|---|---|
| `MV_SERVER` (alias `MV_SERVER_URL`) | `http://localhost:8080` | MangaVeyra server base URL |
| `MV_WORKER_TOKEN` / `MV_WORKER_TOKEN_FILE` | — | Worker token (`mvw_…`), or a file holding it (Docker secrets) |
| `MV_WORKER_NAME` | host name | Name shown in the admin; re-registering with the same name keeps the worker id |
| `MV_BACKENDS` (alias `MV_WORKER_BACKEND`) | `auto` | `auto` or a comma list of `diffusers`, `comfyui`, `vision`, `mock` (dispatch order) |
| `MV_MAX_CONCURRENT` | `1` | Jobs in parallel. Keep 1 per GPU unless your models are small |
| `MV_CLAIM_WAIT_S` | `20` | Long-poll duration of `claim` |
| `MV_DRAIN_TIMEOUT_S` | `120` | On shutdown, how long running jobs may finish before they are handed back |
| `MV_WORKER_DATA_DIR` | `~/.cache/mangaveyra-worker` | Scratch space, LoRA cache, training runs |
| `MV_MODELS_DIR` | packaged catalogue | Directory of local model manifests (`*.yaml`/`*.json`) |
| `MV_MODELS` | all that fit | Only advertise these model ids |
| `MV_LORA_DIR` | — | LoRA files referenced by key |
| `MV_HF_CACHE` | HF default | Hugging Face cache (sets `HF_HOME`) |
| `MV_DEVICE` | `auto` | `auto`, `cuda`, `cuda:1`, `mps`, `cpu` (CPU diffusion only when set explicitly) |
| `MV_OFFLOAD` | `auto` | `auto`, `none`, `model` (model CPU offload), `sequential` |
| `MV_VRAM_BUDGET_MB` | 90 % of the GPU | Budget for resident pipelines |
| `MV_TRAIN_COMMAND` | — | External LoRA trainer command template (see below) |
| `MV_COMFYUI_URL` | — | ComfyUI server, e.g. `http://127.0.0.1:8188` |
| `MV_COMFYUI_WORKFLOWS` | — | Directory of ComfyUI workflow bundles |
| `MV_COMFYUI_TIMEOUT_S` | `1800` | Longest a ComfyUI prompt may run |
| `MV_OCR_ENGINE` / `MV_OCR_URL` | `auto` | `auto`, `manga-ocr`, `tesseract`, `http`, `none` |
| `MV_EMBEDDER` | `auto` | Character Lock embedder: `auto`, `insightface`, `clip`, `structural` |
| `MV_MOCK_MODELS` | `mock` | Model ids the mock backend advertises |
| `MV_MOCK_STEP_DELAY` | `0.05` | Seconds per simulated step (makes progress bars realistic) |
| `MV_MAX_DOWNLOAD_MB` | `64` | Largest input file the worker downloads |
| `MV_TLS_VERIFY` | `true` | Verify the server certificate (disable only on a trusted LAN) |
| `MV_HEALTH_FILE` | — | Touched after each successful server contact (container health checks) |
| `MV_LOG_LEVEL` / `MV_LOG_FORMAT` | `info` / `auto` | Logs are structured: `text` on a terminal, one JSON object per line otherwise |

## Backends

`MV_BACKENDS=auto` enables every backend that can run on the machine, in the order diffusers → comfyui → vision,
then adds the mock: the **full** mock when nothing else can produce artwork (so a fresh install completes every
flow), otherwise only its *honest* helpers (real panel detection and Lanczos upscaling) for job types nothing else
serves. A GPU install never gets placeholder art, fake Character Lock scores or placeholder OCR by accident. Name
backends explicitly to make a missing dependency an error instead.

| Backend | Extra | Job types |
|---|---|---|
| `mock` | — (Pillow only) | every image type, `character.train`, `character.score`, `detect.panels`, `ocr`, `upscale` |
| `diffusers` | `[diffusers]` | `image.generate`, `image.edit`, `image.inpaint`, `sketch.ink`, `character.sheet`, `character.train` |
| `comfyui` | `[comfyui]` | the image types each workflow bundle declares |
| `vision` | `[vision]` | `detect.panels`, `character.score`, `ocr` (when an OCR engine is available) |

`video.animate` and `voice.tts` are not implemented by this worker yet; it never registers job types it cannot run.

### mock

Deterministic, clearly labelled screentone placeholders in the design system's Paper palette: the same inputs and
seed give byte-identical PNGs, and variations differ (tone angle, density, focal point, speed lines, figures). Edits
tone over the input, inpainting fills only the mask, sketch inking cleans the lines with Pillow, character sheets are
front/side/face mannequins. `character.score` returns plausible, deterministic scores (an exact reference copy scores
0.99) and `character.train` uploads a small, valid `.safetensors` marked as a mock. Panel detection and upscaling are
real.

### diffusers

Loads Hugging Face diffusers pipelines with `AutoPipelineForText2Image`, derives img2img / inpainting / ControlNet
pipelines with `from_pipe` (shared weights, no extra VRAM), and reports progress every denoising step with a cheap
latent preview. Per job it can:

- switch to the model's **OpenPose ControlNet** when the job carries a `pose` (OpenPose JSON is rendered to the
  skeleton image ControlNet expects) or a `pose_image`;
- load **LoRAs** (`loras: [{key, weight}]`) and unload them afterwards;
- use an **IP-Adapter** to steer identity from `ref_N` images when the manifest configures one;
- run instruction-edit models (Qwen-Image-Edit, FLUX Kontext) as a separate pipeline (`edit: instruct`).

Pipelines stay resident in a **VRAM-aware LRU cache**; loading another model evicts idle ones until the estimate fits
`MV_VRAM_BUDGET_MB`. Models larger than the GPU run with model or sequential CPU offload (`MV_OFFLOAD=auto`). Models
that would not fit even with offload are not advertised. Output for `bw_screentone` / `flat_grey` styles is converted
to greyscale.

LoRA files are resolved from `files["lora_<i>"]` (a signed URL the server may attach; cached by key under
`MV_WORKER_DATA_DIR/loras`), then the cache, then `MV_LORA_DIR/<key>` or `MV_LORA_DIR/<basename>`.

### comfyui

Runs your own ComfyUI workflows — any LoRA stack, ControlNet or custom node setup. See
[ComfyUI workflows](#comfyui-workflows).

### vision

OpenCV panel detection (contour polygons, also for slanted gutters, compared with the pure-Pillow gutter scan),
speech-bubble candidates, identity scoring and OCR. See [Vision helpers and OCR](#vision-helpers-and-ocr).

## Adding a model

The server decides *which* model runs a job (the router); the worker needs to know *how* to run it. Both use the same
model id. The worker advertises a model only when a manifest with a `worker:` section exists locally and the model
fits the GPU.

1. Make sure the server knows the model (built-in catalogue, or *Admin → Models & providers → Add custom model*).
2. Put a manifest with the same `id` in `MV_MODELS_DIR` (start from
   [`examples/models/my-sdxl-finetune.yaml`](examples/models/my-sdxl-finetune.yaml)). Without `MV_MODELS_DIR` the
   packaged catalogue in `mangaveyra_worker/catalog/` is used: `sdxl-base`, `illustrious-xl`, `animagine-xl-4`,
   `noobai-xl`, `flux2-klein` and `qwen-image-edit`. Weights download from Hugging Face on first use.
3. Check it: `mangaveyra-worker check` lists the model, and
   `mangaveyra-worker job image.generate --model my-sdxl-finetune --input '{"prompt": "Rin Asagiri on the pier"}'`
   renders locally.
4. Restart the worker; it re-registers with the new model list.

Manifest `worker.diffusers` fields:

| Field | Meaning |
|---|---|
| `repo` | Hugging Face repo id or local diffusers folder |
| `single_file` | A `.safetensors` checkpoint (path or URL) for `from_single_file`; `repo` then names the config source |
| `family` | `sdxl` (default), `sd15`, `sd3`, `flux`, `qwen-image`, `auto` — sizes, previews, trainability |
| `dtype` | `auto`, `float16`, `bfloat16`, `float32` |
| `variant`, `revision` | Passed to `from_pretrained` |
| `edit` / `edit_repo` | `img2img` (default) or `instruct` with a separate edit pipeline |
| `inpaint_repo` | Dedicated inpainting checkpoint |
| `controlnet_openpose`, `controlnet_lineart` | ControlNets for posed generation and sketch inking |
| `ip_adapter` | `{repo, subfolder, weight_name, scale}` |
| `default_steps`, `default_guidance`, `default_size`, `max_side`, `multiple_of` | Generation defaults |
| `prompt_suffix`, `negative_prefix`, `scheduler` | Prompt tags and scheduler (`euler_a`, `dpmpp_2m_karras`, …) |
| `trainable`, `train_resolution` | Allow `character.train` with this base (SDXL family) |
| `offload_mode` | `model` or `sequential` when the model does not fit |
| `job_types` | Restrict the job types served with this model |

Top-level `vram_mb` (fully on the GPU) and `min_vram_mb` (with offload) drive advertising and the cache.

## Character LoRA training

`character.train` (`trigger`, `base_model`, `steps`, `rank`, `ref_0…ref_n`) produces a LoRA, two test renders and a
score (test renders vs references, same embedder as Character Lock). Two trainers:

- **Built-in** (default): a compact SDXL/Illustrious LoRA trainer on diffusers + peft — UNet attention LoRA, frozen
  text encoders, cached latents (with flips), AdamW, gradient checkpointing, mixed precision with loss scaling.
  10–20 references, rank 16 and 800–1500 steps take roughly 15–30 minutes on a 16–24 GB GPU; set
  `train_resolution: 768` for 12 GB cards. The worker frees its pipeline cache before training.
- **External command**: set `MV_TRAIN_COMMAND` to any trainer (kohya-ss sd-scripts, OneTrainer, ai-toolkit,
  SimpleTuner). The worker writes the references in the kohya layout (`dataset/10_<trigger>/0000.png` + `.txt`
  captions + `metadata.jsonl`), runs the command, parses progress (`123/1000` counters or `step 123`) and uploads the
  newest `*.safetensors` from the output folder. The template is split into arguments *before* substitution, so
  values cannot inject arguments. Placeholders: `{image_dir} {dataset_dir} {out_dir} {output_name} {steps} {rank}
  {alpha} {lr} {resolution} {seed} {trigger} {base_model} {base_path}`.

```sh
MV_TRAIN_COMMAND="accelerate launch /opt/sd-scripts/sdxl_train_network.py \
  --pretrained_model_name_or_path={base_path} --train_data_dir={dataset_dir} --output_dir={out_dir} \
  --output_name={output_name} --max_train_steps={steps} --network_module=networks.lora \
  --network_dim={rank} --network_alpha={alpha} --learning_rate={lr} --resolution={resolution} \
  --seed={seed} --mixed_precision=bf16 --save_model_as=safetensors --caption_extension=.txt \
  --cache_latents --gradient_checkpointing"
```

Cancel stops the trainer's whole process group.

## ComfyUI workflows

1. In ComfyUI enable dev mode options and export your graph with **Save (API format)**.
2. Write a *bundle* next to it in `MV_COMFYUI_WORKFLOWS` that maps MangaVeyra fields to node inputs (see
   [`examples/comfyui/`](examples/comfyui/)):

```json
{
  "model": "illustrious-comfy",
  "job_types": ["image.generate", "character.sheet"],
  "workflow_file": "sdxl-basic.api.json",
  "inputs": {
    "prompt": "6.inputs.text", "negative": "7.inputs.text", "seed": "3.inputs.seed",
    "steps": "3.inputs.steps", "guidance": "3.inputs.cfg",
    "width": "5.inputs.width", "height": "5.inputs.height",
    "image": "10.inputs.image", "mask": "11.inputs.image", "ref_0": "12.inputs.image", "pose_image": "13.inputs.image"
  },
  "templates": {"prompt": "{prompt}, monochrome, manga, screentone"},
  "outputs": ["9"]
}
```

3. Set `MV_COMFYUI_URL` and restart. The bundle's `model` id is advertised to the server.

Values are `"NODE.inputs.NAME"` (or a list of them). Input files (`image`, `mask`, `sketch`, `ref_N`, `pose_image` —
rendered from the OpenPose JSON when only `pose` is given) are uploaded with `/upload/image` first. Each variation is
queued as its own prompt with `seed + i`. Progress and live previews come from ComfyUI's websocket (polling
`/history` without the `websockets` package). Cancel removes a pending prompt or interrupts it when running. A bundle
can also live inline in a model manifest under `worker.comfyui`. ComfyUI's embedded workflow metadata is stripped
from outputs (it can contain local paths).

## Vision helpers and OCR

- **Panels** (`detect.panels`, `reading: rtl | ltr | vertical`): a pure-Pillow recursive gutter scan works
  everywhere; with OpenCV, frame contours are approximated to polygons (slanted gutters, non-rectangular panels) and
  the result with more panels wins. Reading order is a recursive XY-cut over panel boxes (right-to-left rows for
  manga). A page without structure yields one full-page panel, so imports always produce an editable page.
- **Bubbles**: enclosed, roughly elliptical white regions that contain ink (OpenCV). When an OCR engine is present,
  the vision backend fills their text.
- **Character Lock** (`character.score`): cosine similarity between each candidate and the centroid of the reference
  embeddings. Embedders, best first: InsightFace ArcFace (`pip install insightface`; only when every image has a
  detectable face), OpenCLIP (`pip install open_clip_torch`), and a numpy structural descriptor that always works.
  The output carries `method` so the UI can say which one scored.
- **OCR** (`ocr`), pluggable via `MV_OCR_ENGINE`:
  - `manga-ocr` (Apache-2.0, `pip install manga-ocr`) — Japanese manga text incl. vertical lines, run per bubble;
  - `tesseract` (`pip install pytesseract` + the `tesseract` binary and language packs) — Latin scripts;
  - `http` — POSTs the image (multipart `image`, `lang`) to `MV_OCR_URL` and expects
    `{"lines": [{"text", "box": [x, y, w, h]}]}`. Use this to run GPL tools such as *manga-image-translator* as a
    separate service; MangaVeyra never vendors or links GPL code.

## GPU notes

- **NVIDIA / CUDA**: install the CUDA build of PyTorch that matches your driver *before* the diffusers extra, e.g.
  `pip install --index-url https://download.pytorch.org/whl/cu128 torch torchvision`. The Docker GPU image does this
  and needs the NVIDIA Container Toolkit (`--gpus all`).
- **GPU detection**: `torch.cuda` when PyTorch is already loaded, otherwise `nvidia-smi`; name, total and free VRAM
  of every GPU are reported at registration and refreshed in every heartbeat.
- **VRAM guide**: SDXL-family models (Illustrious, NoobAI, Animagine) run fully on 10–12 GB and with model offload
  on 6–8 GB; FLUX.2 [klein] 4B wants ~14 GB (8 GB with offload); Qwen-Image-Edit needs ~40 GB fully resident and
  uses sequential offload below that (slow but works). LoRA training wants 16 GB+ at 1024 px.
- **Several GPUs**: run one worker per GPU (`CUDA_VISIBLE_DEVICES=1 MV_WORKER_NAME=box-gpu1 mangaveyra-worker`).
- **Apple Silicon**: `MV_DEVICE=mps` works for inference with diffusers; training is CUDA-only.
- **No GPU**: use the mock (every flow works with placeholders) and connect remote providers on the server, or
  run this worker on a rented GPU — it only needs outbound HTTPS to your server.
- **Out of memory** is reported as `oom` (retryable), so the server's router can fall back to another model.

## Protocol behaviour

- `register` on start (and again if the server forgets the worker), a heartbeat every `heartbeat_interval_s` with
  GPU stats and running job ids (the server's `cancel` list stops those jobs), and a long-poll `claim` whenever a
  slot is free. Only job types the worker registered are ever claimed. A `204` that arrives without a long poll
  (the server does this while an admin drains the worker) backs the claim loop off (0.5 s → 5 s) instead of
  spinning. A job with `model.id` `""` runs on any backend that serves its type.
- Progress is throttled to about two requests per second and doubles as the lease extension; a job that is quiet
  (model loading, long uploads) gets keep-alive progress every third of its lease. `cancel: true` stops the job at
  the next step and it is reported with `fail` code `canceled`.
- Every output file is uploaded with `artifacts` before `complete`; `actual.gpu_seconds` is the accelerator time.
  Job calls carry an `X-Worker-Id` header so the server can verify the lease holder.
- Network errors, 5xx and 429 are retried with exponential backoff and jitter; results (`upload`, `complete`,
  `fail`) are retried for up to five minutes so finished work survives a server restart (a second signal stops
  them). `409 lease_lost` — or `403`/`404`/`410` on a job call — means the server has moved the job on (re-queued
  after missed heartbeats, canceled, deleted with its worker): the job is dropped without a `fail` report.
- Failures map to protocol codes: invalid input → `bad_input`, OOM → `oom`, time limits → `timeout`, cancel →
  `canceled`, anything else → `internal` (retryable).
- `SIGINT`/`SIGTERM`: stop claiming (an in-flight long poll is abandoned at once), let running jobs finish for
  `MV_DRAIN_TIMEOUT_S`, then hand them back as retryable. A second signal cancels immediately.

## Development

```sh
python3 -m venv /tmp/mvw-venv && . /tmp/mvw-venv/bin/activate
pip install -e 'worker[test]'
cd worker && pytest -q
```

Tests run without torch: a fake MangaVeyra server (threaded `http.server`) drives the real client and runner through
register → claim → progress → artifacts → complete/fail, cancel (progress and heartbeat), backoff, re-registration,
concurrency, lease keep-alive and graceful shutdown; a fake ComfyUI covers the ComfyUI backend (HTTP and websocket);
the mock outputs, panel detection on synthetic pages, identity scoring, the external trainer and the diffusers
helpers (specs, VRAM cache, LoRA resolution) are unit-tested. With the diffusers extra installed,
`MV_TEST_DIFFUSERS=1 pytest tests/test_diffusers_backend.py` runs real pipelines and a LoRA training run on a tiny
SDXL test model from the Hugging Face Hub on CPU.

Sample content in tests and docs uses MangaVeyra's own demo world (*Tidebreak Courier*: Rin Asagiri, Kaito Mbeki,
Old Wren, Sora Vell, Pip the Gull).
