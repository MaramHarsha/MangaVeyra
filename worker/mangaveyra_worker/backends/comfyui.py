"""ComfyUI backend: run user-exported workflows through the ComfyUI HTTP API.

A *workflow bundle* pairs a workflow exported with **Save (API format)** with an input mapping that says which
node input receives each MangaVeyra field. Bundles live in ``MV_COMFYUI_WORKFLOWS`` (``*.json`` / ``*.yaml``) or
inline in a model manifest under ``worker.comfyui``::

    {
      "model": "illustrious-comfy",            # model id advertised to the server (default: file name)
      "job_types": ["image.generate"],         # image job types this workflow implements
      "workflow_file": "illustrious.api.json", # or "workflow": { …API-format graph… }
      "inputs": {                              # field → "node_id.inputs.name" (or a list of them)
        "prompt": "6.inputs.text",
        "negative": "7.inputs.text",
        "seed": "3.inputs.seed",
        "steps": "3.inputs.steps",
        "guidance": "3.inputs.cfg",
        "width": "5.inputs.width",
        "height": "5.inputs.height",
        "strength": "3.inputs.denoise",
        "image": "10.inputs.image",            # input files are uploaded via /upload/image first
        "mask": "11.inputs.image",
        "ref_0": "12.inputs.image",
        "pose_image": "13.inputs.image"        # rendered from the OpenPose JSON when only `pose` is given
      },
      "templates": {"prompt": "{prompt}, monochrome manga, screentone"},
      "outputs": ["9"]                         # SaveImage node ids (default: every node that saved images)
    }

Each variation (``n``) is queued as its own prompt with ``seed + i``. Progress comes from ComfyUI's websocket
(``/ws?clientId=…``; step progress and live previews); without the ``websockets`` package the backend polls
``/history``. Cancel removes a pending prompt from the queue or interrupts it when it is running.
"""

from __future__ import annotations

import copy
import io
import json
import logging
import time
import uuid
from pathlib import Path
from typing import Any, Mapping

import httpx
import yaml
from PIL import Image

from mangaveyra_worker import imaging
from mangaveyra_worker.backends.base import Backend
from mangaveyra_worker.errors import BadInput, JobCanceled, JobFailure, JobTimeout, OutOfMemory
from mangaveyra_worker.jobs import (
    IMAGE_JOB_TYPES,
    Job,
    JobContext,
    JobResult,
    clamp_int,
    decode_image,
    encode_png,
    resolve_seed,
)
from mangaveyra_worker.manifests import ModelCatalog

log = logging.getLogger(__name__)

#: Input fields forwarded to the workflow as plain values.
VALUE_FIELDS = (
    "prompt",
    "negative",
    "seed",
    "steps",
    "guidance",
    "width",
    "height",
    "strength",
    "feather_px",
    "style",
    "finish",
    "scale",
)
#: Input files that may be mapped (plus any ``ref_N``/``reference_N``).
FILE_FIELDS = ("image", "mask", "sketch", "pose_image")


class WorkflowBundle:
    """A validated workflow + input mapping."""

    def __init__(self, data: Mapping[str, Any], *, source: str, base_dir: Path | None = None) -> None:
        self.source = source
        workflow = data.get("workflow")
        if workflow is None and data.get("workflow_file"):
            path = Path(str(data["workflow_file"]))
            if not path.is_absolute() and base_dir is not None:
                path = base_dir / path
            workflow = json.loads(path.read_text(encoding="utf-8"))
        if not isinstance(workflow, Mapping) or not workflow:
            raise ValueError(f"{source}: bundle needs a 'workflow' object or 'workflow_file'")
        if "nodes" in workflow and "links" in workflow:
            raise ValueError(f"{source}: this is a UI-format workflow; export it with 'Save (API format)'")
        self.workflow: dict[str, Any] = dict(workflow)
        self.model = str(data.get("model") or Path(source).stem)
        self.job_types = [str(t) for t in (data.get("job_types") or ["image.generate"])]
        bad = [t for t in self.job_types if t not in IMAGE_JOB_TYPES]
        if bad:
            raise ValueError(f"{source}: ComfyUI bundles only serve image job types, not {bad}")
        self.inputs: dict[str, list[str]] = {}
        for field, target in (data.get("inputs") or {}).items():
            targets = [target] if isinstance(target, str) else list(target)
            for t in targets:
                self._resolve(self.workflow, t)  # validate now, not on the first job
            self.inputs[str(field)] = targets
        self.templates = {str(k): str(v) for k, v in (data.get("templates") or {}).items()}
        self.outputs = [str(o) for o in (data.get("outputs") or [])]
        for node in self.outputs:
            if node not in self.workflow:
                raise ValueError(f"{source}: output node {node!r} is not in the workflow")

    @staticmethod
    def _resolve(workflow: Mapping[str, Any], target: str) -> tuple[dict[str, Any], str]:
        parts = target.split(".")
        if len(parts) != 3 or parts[1] != "inputs":
            raise ValueError(f"mapping target must look like 'NODE.inputs.NAME', got {target!r}")
        node = workflow.get(parts[0])
        if not isinstance(node, Mapping) or "inputs" not in node:
            raise ValueError(f"mapping target {target!r}: node {parts[0]!r} not found")
        return node["inputs"], parts[2]  # type: ignore[return-value]

    def render(self, values: Mapping[str, Any]) -> dict[str, Any]:
        """Return a copy of the workflow with every mapped field set from ``values``."""
        wf = copy.deepcopy(self.workflow)
        for field, targets in self.inputs.items():
            if field not in values or values[field] is None:
                continue
            value = values[field]
            if field in self.templates and isinstance(value, str):
                value = self.templates[field].replace("{" + field + "}", value)
            for target in targets:
                inputs, name = self._resolve(wf, target)
                inputs[name] = value
        return wf


def load_bundles(directory: Path | None, catalog: ModelCatalog) -> dict[str, WorkflowBundle]:
    """Load bundles from a directory and from manifests with an inline ``worker.comfyui`` section."""
    bundles: dict[str, WorkflowBundle] = {}
    if directory is not None and directory.is_dir():
        for path in sorted(directory.iterdir()):
            if path.suffix not in (".json", ".yaml", ".yml") or path.name.endswith(".api.json"):
                continue
            try:
                text = path.read_text(encoding="utf-8")
                data = json.loads(text) if path.suffix == ".json" else yaml.safe_load(text)
                if not isinstance(data, Mapping) or "inputs" not in data:
                    continue  # a bare workflow file referenced by a bundle
                b = WorkflowBundle(data, source=str(path), base_dir=directory)
                bundles[b.model] = b
            except (OSError, ValueError, yaml.YAMLError) as exc:
                log.warning(
                    "skipping ComfyUI bundle", extra={"fields": {"file": str(path), "error": str(exc)}}
                )
    for manifest in catalog.with_section("comfyui"):
        section = manifest["worker"]["comfyui"]
        try:
            if section.get("bundle"):
                if directory is None:
                    raise ValueError("bundle reference needs MV_COMFYUI_WORKFLOWS")
                path = directory / str(section["bundle"])
                data = yaml.safe_load(path.read_text(encoding="utf-8"))
                data = {**data, "model": manifest["id"]}
                b = WorkflowBundle(data, source=str(path), base_dir=directory)
            else:
                b = WorkflowBundle(
                    {**section, "model": manifest["id"]},
                    source=f"manifest:{manifest['id']}",
                    base_dir=directory,
                )
            bundles[b.model] = b
        except (OSError, ValueError, yaml.YAMLError) as exc:
            log.warning(
                "skipping ComfyUI manifest", extra={"fields": {"model": manifest["id"], "error": str(exc)}}
            )
    return bundles


class ComfyUIClient:
    """Minimal ComfyUI HTTP/websocket client."""

    def __init__(
        self, base_url: str, *, timeout: float = 60.0, transport: httpx.BaseTransport | None = None
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.client_id = uuid.uuid4().hex
        self.http = httpx.Client(base_url=self.base_url, timeout=timeout, transport=transport)

    def system_stats(self) -> dict[str, Any]:
        """``GET /system_stats``."""
        r = self.http.get("/system_stats")
        r.raise_for_status()
        return r.json()

    def upload_image(self, data: bytes, filename: str) -> str:
        """``POST /upload/image``; returns the value a LoadImage node expects (``subfolder/name``)."""
        r = self.http.post(
            "/upload/image",
            files={"image": (filename, data, "image/png")},
            data={"overwrite": "true", "type": "input"},
        )
        r.raise_for_status()
        doc = r.json()
        name, sub = doc.get("name") or filename, doc.get("subfolder") or ""
        return f"{sub}/{name}" if sub else name

    def queue_prompt(self, workflow: Mapping[str, Any]) -> str:
        """``POST /prompt``; returns the prompt id. Validation errors become :class:`BadInput`."""
        r = self.http.post("/prompt", json={"prompt": workflow, "client_id": self.client_id})
        if r.status_code == 400:
            try:
                err = r.json()
                detail = err.get("error", {}).get("message") or json.dumps(err.get("node_errors"))[:500]
            except ValueError:
                detail = r.text[:500]
            raise BadInput(f"ComfyUI rejected the workflow: {detail}")
        r.raise_for_status()
        return str(r.json()["prompt_id"])

    def history(self, prompt_id: str) -> dict[str, Any] | None:
        """``GET /history/{id}``; ``None`` until the prompt has finished."""
        r = self.http.get(f"/history/{prompt_id}")
        r.raise_for_status()
        return (r.json() or {}).get(prompt_id)

    def view(self, filename: str, subfolder: str, kind: str) -> bytes:
        """``GET /view`` for an output image."""
        r = self.http.get("/view", params={"filename": filename, "subfolder": subfolder, "type": kind})
        r.raise_for_status()
        return r.content

    def cancel(self, prompt_id: str) -> None:
        """Remove a pending prompt, or interrupt it if it is running."""
        try:
            q = self.http.get("/queue").json()
            running = {str(item[1]) for item in q.get("queue_running") or []}
            pending = {str(item[1]) for item in q.get("queue_pending") or []}
            if prompt_id in pending:
                self.http.post("/queue", json={"delete": [prompt_id]})
            if prompt_id in running:
                self.http.post("/interrupt", json={"prompt_id": prompt_id})
        except (httpx.HTTPError, ValueError) as exc:
            log.warning("ComfyUI cancel failed", extra={"fields": {"prompt": prompt_id, "error": str(exc)}})

    def ws_url(self) -> str:
        """Websocket URL for this client id."""
        return (
            self.base_url.replace("https://", "wss://").replace("http://", "ws://")
            + f"/ws?clientId={self.client_id}"
        )

    def close(self) -> None:
        """Close the HTTP pool."""
        self.http.close()


class ComfyUIBackend(Backend):
    """Runs workflow bundles on a ComfyUI server."""

    name = "comfyui"

    @classmethod
    def available(cls, config: Any) -> tuple[bool, str]:
        """Needs ``MV_COMFYUI_URL``."""
        if not config.comfyui_url:
            return False, "MV_COMFYUI_URL is not set"
        return True, ""

    def __init__(
        self, config: Any, gpus: list[dict[str, Any]], *, transport: httpx.BaseTransport | None = None
    ) -> None:
        super().__init__(config, gpus)
        self.catalog = ModelCatalog.load(config.models_dir) if config.models_dir else ModelCatalog()
        self.bundles = load_bundles(config.comfyui_workflows_dir, self.catalog)
        if config.models:
            self.bundles = {k: v for k, v in self.bundles.items() if k in config.models}
        self.api = ComfyUIClient(config.comfyui_url, transport=transport)
        try:
            stats = self.api.system_stats()
            log.info(
                "ComfyUI reachable",
                extra={
                    "fields": {
                        "url": config.comfyui_url,
                        "version": stats.get("system", {}).get("comfyui_version"),
                    }
                },
            )
        except (httpx.HTTPError, ValueError) as exc:
            log.warning(
                "ComfyUI not reachable yet; jobs will fail over until it is",
                extra={"fields": {"url": config.comfyui_url, "error": str(exc)}},
            )

    def job_types(self) -> list[str]:
        """Image job types implemented by at least one bundle."""
        seen: dict[str, None] = {}
        for b in self.bundles.values():
            for t in b.job_types:
                seen[t] = None
        return list(seen)

    def models(self) -> list[str]:
        """Bundle model ids."""
        return sorted(self.bundles)

    def accepts(self, job: Job) -> bool:
        """A bundle must exist for the job's model and type."""
        b = self.bundles.get(job.model_id or "")
        if b is None and job.model_id is None and len(self.bundles) == 1:
            b = next(iter(self.bundles.values()))
        return b is not None and job.type in b.job_types

    def _bundle(self, job: Job) -> WorkflowBundle:
        b = self.bundles.get(job.model_id or "")
        if b is None and job.model_id is None and len(self.bundles) == 1:
            b = next(iter(self.bundles.values()))
        if b is None:
            raise BadInput(f"no ComfyUI workflow for model {job.model_id}")
        return b

    def run(self, job: Job, ctx: JobContext) -> JobResult:
        """Queue one prompt per variation and collect the images."""
        bundle = self._bundle(job)
        inp = job.input
        n = clamp_int(inp.get("n"), 1, 1, 4, "n")
        seed = resolve_seed(inp.get("seed"))
        values: dict[str, Any] = {k: inp[k] for k in VALUE_FIELDS if k in inp}
        values.update(self._upload_files(job, ctx, bundle))
        images: list[dict[str, Any]] = []
        gpu = 0.0
        for i in range(n):
            s = (seed + i) % 2**32
            values["seed"] = s
            wf = bundle.render(values)
            started = time.monotonic()
            outputs = self._execute(ctx, wf, bundle, variation=i, total=n)
            gpu += time.monotonic() - started
            for j, data in enumerate(outputs):
                img = decode_image(data, name="comfyui output", mode=None)
                name = f"variation-{len(images) + 1}"
                key = ctx.upload(
                    name,
                    _png(img),
                    filename=f"{name}.png",
                    meta={"seed": s, "workflow": bundle.model, "output_index": j},
                )
                images.append({"key": key, "seed": s})
        return JobResult({"images": images}, gpu_seconds=gpu if self.gpus else 0.0)

    def _upload_files(self, job: Job, ctx: JobContext, bundle: WorkflowBundle) -> dict[str, str]:
        values: dict[str, str] = {}
        wanted = [f for f in bundle.inputs if f in job.files or f in FILE_FIELDS]
        for field in wanted:
            if field in job.files:
                data = ctx.file_bytes(field)
            elif field == "pose_image" and isinstance(job.input.get("pose"), dict):
                w = clamp_int(job.input.get("width"), 1024, 64, 4096, "width")
                h = clamp_int(job.input.get("height"), 1024, 64, 4096, "height")
                data = encode_png(imaging.render_openpose(job.input["pose"], w, h))
            else:
                continue
            ctx.check()
            values[field] = self.api.upload_image(data, f"mv-{job.id[-10:]}-{field}.png")
        return values

    def _execute(
        self, ctx: JobContext, wf: Mapping[str, Any], bundle: WorkflowBundle, *, variation: int, total: int
    ) -> list[bytes]:
        prompt_id = self.api.queue_prompt(wf)
        ctx.log.info(
            "ComfyUI prompt queued", extra={"fields": {"prompt": prompt_id, "variation": variation + 1}}
        )
        deadline = time.monotonic() + self.config.comfyui_timeout_s
        try:
            entry = self._wait(ctx, prompt_id, deadline, variation, total)
        except JobCanceled:
            self.api.cancel(prompt_id)
            raise
        status = entry.get("status") or {}
        if status.get("status_str") == "error":
            raise _execution_error(status)
        outputs = entry.get("outputs") or {}
        nodes = bundle.outputs or list(outputs)
        files: list[dict[str, Any]] = []
        for node in nodes:
            files.extend((outputs.get(node) or {}).get("images") or [])
        final = [f for f in files if f.get("type") == "output"] or files
        if not final:
            raise JobFailure(
                "ComfyUI finished without output images (check the bundle's output nodes)",
                code="internal",
                retryable=False,
            )
        return [self.api.view(f["filename"], f.get("subfolder", ""), f.get("type", "output")) for f in final]

    def _wait(
        self, ctx: JobContext, prompt_id: str, deadline: float, variation: int, total: int
    ) -> dict[str, Any]:
        """Wait for the prompt: websocket progress when available, else polling ``/history``."""
        try:
            from websockets.sync.client import connect
        except ImportError:
            connect = None
        if connect is not None:
            try:
                with connect(self.api.ws_url(), open_timeout=10, max_size=None) as ws:
                    return self._wait_ws(ctx, ws, prompt_id, deadline, variation, total)
            except (OSError, TimeoutError) as exc:
                ctx.log.warning(
                    "ComfyUI websocket unavailable, polling", extra={"fields": {"error": str(exc)}}
                )
            except Exception as exc:  # websockets.ConnectionClosed and friends
                if isinstance(exc, (JobCanceled, JobFailure)):
                    raise
                ctx.log.warning("ComfyUI websocket dropped, polling", extra={"fields": {"error": str(exc)}})
        return self._wait_poll(ctx, prompt_id, deadline, variation, total)

    def _wait_ws(
        self, ctx: JobContext, ws: Any, prompt_id: str, deadline: float, variation: int, total: int
    ) -> dict[str, Any]:
        mine = False
        while True:
            if time.monotonic() > deadline:
                self.api.cancel(prompt_id)
                raise JobTimeout("ComfyUI prompt timed out")
            ctx.check()
            # The prompt may have finished before the socket connected.
            try:
                msg = ws.recv(timeout=1.0)
            except TimeoutError:
                entry = self.api.history(prompt_id)
                if _finished(entry):
                    return entry
                ctx.progress(
                    (variation + (0.02 if not mine else 0.05)) / total, message="waiting for ComfyUI"
                )
                continue
            if isinstance(msg, bytes):
                if mine and len(msg) > 8 and int.from_bytes(msg[:4], "big") == 1:
                    try:
                        preview = Image.open(io.BytesIO(msg[8:]))
                        preview.load()
                        ctx.progress((variation + 0.5) / total, message="denoising", preview=preview)
                    except Exception:  # previews are best effort
                        pass
                continue
            try:
                doc = json.loads(msg)
            except ValueError:
                continue
            data = doc.get("data") or {}
            if data.get("prompt_id") not in (None, prompt_id):
                continue
            kind = doc.get("type")
            if kind == "execution_start":
                mine = True
            elif kind == "progress" and data.get("prompt_id", prompt_id) == prompt_id:
                mine = True
                value, maximum = int(data.get("value") or 0), int(data.get("max") or 1)
                ctx.progress(
                    (variation + value / max(1, maximum)) / total,
                    step=value,
                    steps=maximum,
                    message="denoising",
                )
            elif kind == "execution_error":
                raise _execution_error({"messages": [["execution_error", data]]})
            elif kind == "execution_interrupted":
                raise JobCanceled("ComfyUI prompt interrupted")
            elif kind in ("executing", "execution_success") and (
                kind == "execution_success" or data.get("node") is None
            ):
                if data.get("prompt_id") == prompt_id:
                    entry = self.api.history(prompt_id)
                    if entry is not None:
                        return entry

    def _wait_poll(
        self, ctx: JobContext, prompt_id: str, deadline: float, variation: int, total: int
    ) -> dict[str, Any]:
        while True:
            entry = self.api.history(prompt_id)
            if _finished(entry):
                return entry
            if time.monotonic() > deadline:
                self.api.cancel(prompt_id)
                raise JobTimeout("ComfyUI prompt timed out")
            ctx.progress((variation + 0.1) / total, message="waiting for ComfyUI")
            if ctx.cancel_event.wait(1.0):
                ctx.check()

    def close(self) -> None:
        """Close the HTTP client."""
        self.api.close()


def _finished(entry: Mapping[str, Any] | None) -> bool:
    """Whether a ``/history`` entry describes a finished prompt (successfully or not)."""
    if entry is None:
        return False
    status = entry.get("status") or {}
    return not status or bool(status.get("completed")) or status.get("status_str") in ("success", "error")


def _png(img: Image.Image) -> bytes:
    """Re-encode an output without ComfyUI's ``prompt``/``workflow`` text chunks (they can leak local paths)."""
    if img.mode not in ("RGB", "RGBA", "L"):
        img = img.convert("RGB")
    return encode_png(img, text={"Software": "MangaVeyra worker (ComfyUI)"})


def _execution_error(status: Mapping[str, Any]) -> JobFailure:
    text = json.dumps(status.get("messages") or status)[:800]
    if "OutOfMemory" in text or "out of memory" in text.lower():
        return OutOfMemory(f"ComfyUI ran out of memory: {text}")
    return JobFailure(f"ComfyUI execution error: {text}", code="internal", retryable=True)
