"""The built-in mock backend: every job type the product needs, with Pillow only.

Image jobs return deterministic, clearly labelled screentone placeholders (same inputs + seed → identical PNG
bytes; variations differ). Vision jobs are *real* where that is cheap: panel detection runs the actual detectors,
upscaling is a real Lanczos resize. Character scores are fake but plausible and deterministic. The mock simulates
denoising steps (``MV_MOCK_STEP_DELAY``) so progress bars, cancel and previews behave like a real GPU run.
"""

from __future__ import annotations

import hashlib
import struct
import time
from typing import Any

from PIL import Image, ImageDraw, ImageFilter

from mangaveyra_worker import imaging
from mangaveyra_worker.backends.base import Backend
from mangaveyra_worker.errors import BadInput
from mangaveyra_worker.jobs import (
    Job,
    JobContext,
    JobResult,
    clamp_float,
    clamp_int,
    resolve_seed,
    validate_trigger,
)

JOB_TYPES = [
    "image.generate",
    "image.edit",
    "image.inpaint",
    "sketch.ink",
    "character.sheet",
    "character.train",
    "character.score",
    "detect.panels",
    "ocr",
    "upscale",
]

MAX_SIDE = 4096


def _frac(*parts: bytes) -> float:
    h = hashlib.sha256(b"".join(parts)).digest()
    return int.from_bytes(h[:8], "big") / 2**64


class MockBackend(Backend):
    """Placeholder generation and cheap real vision helpers; needs no GPU and no model downloads."""

    name = "mock"

    def __init__(self, config: Any, gpus: list[dict[str, Any]], *, only: set[str] | None = None) -> None:
        super().__init__(config, gpus)
        self._only = set(only) if only is not None else None

    def job_types(self) -> list[str]:
        """Every job type except video and voice (or the ``only`` subset)."""
        return [t for t in JOB_TYPES if self._only is None or t in self._only]

    def models(self) -> list[str]:
        """``MV_MOCK_MODELS`` (default ``mock``)."""
        return list(self.config.mock_models)

    # ------------------------------------------------------------------ dispatch

    def run(self, job: Job, ctx: JobContext) -> JobResult:
        """Run one job."""
        handler = {
            "image.generate": self._generate,
            "image.edit": self._edit,
            "image.inpaint": self._inpaint,
            "sketch.ink": self._sketch,
            "character.sheet": self._sheet,
            "character.train": self._train,
            "character.score": self._score,
            "detect.panels": self._panels,
            "ocr": self._ocr,
            "upscale": self._upscale,
        }.get(job.type)
        if handler is None:
            raise BadInput(f"mock backend cannot run {job.type}")
        return JobResult(handler(job, ctx), gpu_seconds=0.0)

    # ------------------------------------------------------------------ helpers

    def _simulate(
        self,
        ctx: JobContext,
        steps: int,
        *,
        offset: int = 0,
        total: int | None = None,
        message: str = "denoising",
        preview: Image.Image | None = None,
    ) -> None:
        """Walk through ``steps`` fake steps, reporting progress and honouring cancel."""
        total = total or steps
        delay = self.config.mock_step_delay
        for s in range(1, steps + 1):
            if delay > 0:
                time.sleep(delay)
            done = offset + s
            show = preview if preview is not None and s == steps // 2 else None
            ctx.progress(min(0.99, done / total), step=done, steps=total, message=message, preview=show)

    def _variations(
        self,
        job: Job,
        ctx: JobContext,
        render: Any,
        *,
        n: int,
        seed: int,
        steps: int,
        extra_meta: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        images = []
        for i in range(n):
            s = (seed + i) % 2**32
            img = render(i, s)
            self._simulate(ctx, steps, offset=i * steps, total=n * steps, preview=img)
            meta = {
                "seed": s,
                "variation": i + 1,
                "width": img.width,
                "height": img.height,
                "placeholder": True,
            }
            meta.update(extra_meta or {})
            key = ctx.upload_image(f"variation-{i + 1}", img, meta=meta)
            images.append({"key": key, "seed": s})
        return {"images": images}

    def _label(self, job: Job) -> str:
        return job.type if not job.model_id else f"{job.type} · {job.model_id}"

    # ------------------------------------------------------------------ image jobs

    def _generate(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        inp = job.input
        width = clamp_int(inp.get("width"), 1024, 64, MAX_SIDE, "width")
        height = clamp_int(inp.get("height"), 1024, 64, MAX_SIDE, "height")
        n = clamp_int(inp.get("n"), 1, 1, 4, "n")
        steps = clamp_int(inp.get("steps"), 20, 1, 200, "steps")
        style = str(inp.get("style") or "bw_screentone")
        seed = resolve_seed(inp.get("seed"))
        prompt = str(inp.get("prompt") or "")
        pose = None
        if ctx.has_file("pose_image"):
            pose = ctx.image("pose_image").resize((width, height))
        elif isinstance(inp.get("pose"), dict):
            pose = imaging.render_openpose(inp["pose"], width, height)
        refs = len(ctx.file_names("ref_"))

        def render(i: int, s: int) -> Image.Image:
            img = imaging.placeholder(
                width,
                height,
                seed=s,
                job_type=self._label(job),
                prompt=prompt,
                style=style,
                variation=i + 1,
                total=n,
            )
            if pose is not None:
                # Show the pose was honoured: the skeleton's coloured limbs over the placeholder.
                mask = pose.convert("L").point(lambda v: 255 if v > 20 else 0)
                img.paste(pose, mask=mask)
            if refs:
                imaging.label_chip_at(img, img.width * 0.72, img.height * 0.03, f"REFS {refs}")
            return img

        return self._variations(job, ctx, render, n=n, seed=seed, steps=steps)

    def _edit(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        inp = job.input
        n = clamp_int(inp.get("n"), 1, 1, 4, "n")
        strength = clamp_float(inp.get("strength"), 0.6, 0.0, 1.0, "strength")
        seed = resolve_seed(inp.get("seed"))
        base = ctx.image("image")

        def render(i: int, s: int) -> Image.Image:
            img = imaging.tone_overlay(base, seed=s, style="bw_screentone", strength=0.15 + 0.5 * strength)
            imaging.label_chip(img, f"PLACEHOLDER EDIT · V{i + 1}/{n} · SEED {s}", corner="bl")
            return img

        return self._variations(job, ctx, render, n=n, seed=seed, steps=12)

    def _inpaint(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        inp = job.input
        n = clamp_int(inp.get("n"), 1, 1, 4, "n")
        feather = clamp_int(inp.get("feather_px"), 8, 0, 256, "feather_px")
        seed = resolve_seed(inp.get("seed"))
        base = ctx.image("image")
        mask = ctx.image("mask", mode="L")
        if mask.size != base.size:
            mask = mask.resize(base.size, Image.Resampling.NEAREST)
        soft = mask.filter(ImageFilter.GaussianBlur(feather / 2)) if feather else mask
        prompt = str(inp.get("prompt") or "")

        def render(i: int, s: int) -> Image.Image:
            fill = imaging.placeholder(
                base.width,
                base.height,
                seed=s,
                job_type=self._label(job),
                prompt="",
                variation=i + 1,
                total=n,
                subject="none",
            )
            out = Image.composite(fill, base, soft)
            box = mask.getbbox()
            if box and prompt:
                imaging.label_chip_at(out, box[0] + 4, box[1] + 4, imaging.wrap_prompt(prompt, 360, 12, 1))
            return out

        return self._variations(job, ctx, render, n=n, seed=seed, steps=12)

    def _sketch(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        inp = job.input
        strength = clamp_float(inp.get("strength"), 0.7, 0.0, 1.0, "strength")
        finish = str(inp.get("finish") or "screentone")
        n = clamp_int(inp.get("n"), 1, 1, 4, "n")
        seed = resolve_seed(inp.get("seed"))
        sketch = ctx.image("sketch")

        def render(i: int, s: int) -> Image.Image:
            img = imaging.ink_sketch(sketch, strength=strength, finish=finish, seed=s)
            imaging.label_chip(img, f"PLACEHOLDER INK · {finish.upper()} · V{i + 1}/{n}", corner="bl")
            return img

        return self._variations(job, ctx, render, n=n, seed=seed, steps=16)

    def _sheet(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        inp = job.input
        n = clamp_int(inp.get("n"), 1, 1, 4, "n")
        seed = resolve_seed(inp.get("seed"))
        style = str(inp.get("style") or "bw_screentone")
        prompt = str(inp.get("prompt") or "")

        def render(i: int, s: int) -> Image.Image:
            return imaging.placeholder(
                1536,
                1024,
                seed=s,
                job_type=self._label(job),
                prompt=prompt,
                style=style,
                variation=i + 1,
                total=n,
                subject="sheet",
            )

        return self._variations(job, ctx, render, n=n, seed=seed, steps=20)

    def _upscale(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        scale = clamp_int(job.input.get("scale"), 2, 2, 4, "scale")
        if scale not in (2, 4):
            raise BadInput("scale must be 2 or 4")
        img = ctx.image("image")
        if max(img.size) * scale > 8192:
            raise BadInput("upscaled image would exceed 8192 px")
        ctx.progress(0.3, message="resampling", force=True)
        out = img.resize((img.width * scale, img.height * scale), Image.Resampling.LANCZOS)
        key = ctx.upload_image(
            "upscaled", out, meta={"scale": scale, "method": "lanczos", "placeholder": True}
        )
        return {"images": [{"key": key, "seed": None}]}

    # ------------------------------------------------------------------ character jobs

    def _train(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        inp = job.input
        trigger = validate_trigger(inp.get("trigger"))
        steps = clamp_int(inp.get("steps"), 800, 1, 20000, "steps")
        rank = clamp_int(inp.get("rank"), 16, 1, 256, "rank")
        refs = ctx.file_names("ref_")
        if not refs:
            raise BadInput("character.train needs at least one ref_N image")
        digest = hashlib.sha256()
        for name in refs:
            digest.update(ctx.file_bytes(name))
        # Simulated optimisation loop: at most 40 progress ticks however many steps were requested.
        ticks = min(steps, 40)
        delay = self.config.mock_step_delay
        for t in range(1, ticks + 1):
            if delay > 0:
                time.sleep(delay)
            ctx.progress(
                0.85 * t / ticks, step=round(steps * t / ticks), steps=steps, message="training LoRA"
            )
        tensor = struct.pack("<4f", 0.0, 0.0, 0.0, 0.0)
        blob = imaging.write_safetensors(
            {"mock.lora_down.weight": ("F32", [1, 4], tensor)},
            {
                "mangaveyra.mock": "true",
                "trigger": trigger,
                "base_model": str(inp.get("base_model") or ""),
                "rank": str(rank),
                "steps": str(steps),
                "refs_sha256": digest.hexdigest(),
            },
        )
        lora_key = ctx.upload(
            "lora",
            blob,
            content_type="application/octet-stream",
            filename=f"{trigger}.safetensors",
            meta={"placeholder": True, "trigger": trigger},
        )
        tests = []
        seed = int(digest.hexdigest()[:8], 16)
        for i in range(2):
            img = imaging.placeholder(
                768,
                1024,
                seed=seed + i,
                job_type="character.train test",
                prompt=trigger,
                variation=i + 1,
                total=2,
            )
            tests.append(
                {"key": ctx.upload_image(f"test-{i + 1}", img, meta={"placeholder": True}), "seed": seed + i}
            )
            ctx.progress(0.9 + 0.05 * (i + 1), message="test images")
        score = round(0.86 + 0.1 * _frac(digest.digest()), 3)
        return {"lora": {"key": lora_key}, "test_images": tests, "score": score}

    def _score(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        refs = ctx.file_names("reference_")
        cands = ctx.file_names("candidate_")
        if not refs or not cands:
            raise BadInput("character.score needs reference_N and candidate_N files")
        ref_bytes = [ctx.file_bytes(n) for n in refs]
        ref_digests = {hashlib.sha256(b).digest() for b in ref_bytes}
        anchor = hashlib.sha256(b"".join(ref_bytes)).digest()
        scores = []
        for i, name in enumerate(cands):
            data = ctx.file_bytes(name)
            ctx.progress((i + 1) / len(cands), step=i + 1, steps=len(cands), message="scoring")
            if hashlib.sha256(data).digest() in ref_digests:
                scores.append(0.99)
            else:
                # Plausible Character Lock range: most candidates pass, a few drift below 0.8.
                scores.append(round(0.72 + 0.25 * _frac(anchor, data), 3))
        return {"scores": scores, "method": "mock"}

    # ------------------------------------------------------------------ vision jobs

    def _panels(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        from mangaveyra_worker.vision.panels import detect_bubbles, detect_panels

        reading = str(job.input.get("reading") or "rtl")
        if reading not in ("rtl", "ltr", "vertical"):
            raise BadInput("reading must be rtl, ltr or vertical")
        page = ctx.image("page")
        ctx.progress(0.2, message="detecting panels", force=True)
        panels = detect_panels(page, reading)
        ctx.progress(0.7, message="detecting bubbles")
        return {"panels": panels, "bubbles": detect_bubbles(page)}

    def _ocr(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        from mangaveyra_worker.vision.ocr import _bubble_boxes, make_ocr_engine

        lang = str(job.input.get("lang") or "ja")
        img = ctx.image("image")
        ctx.progress(0.2, message="reading text", force=True)
        engine = None
        if self.config.ocr_engine != "none":
            try:
                engine = make_ocr_engine(self.config.ocr_engine, self.config.ocr_url)
            except Exception as exc:  # a configured engine that is missing: fall back to the placeholder
                ctx.log.warning(
                    "OCR engine unavailable, using placeholder", extra={"fields": {"error": str(exc)}}
                )
        if engine is not None:
            return {"lines": engine.recognise(img, lang), "engine": engine.name}
        boxes = _bubble_boxes(img) or [[0, 0, img.width, img.height]]
        return {"lines": [{"text": "[placeholder OCR]", "box": b} for b in boxes], "engine": "mock"}


def draw_debug_panels(img: Image.Image, panels: list[dict[str, Any]]) -> Image.Image:
    """Overlay detected panel polygons and their order (handy when tuning the detector)."""
    out = img.convert("RGB").copy()
    draw = ImageDraw.Draw(out)
    for p in panels:
        pts = [tuple(pt) for pt in p["points"]]
        draw.polygon(pts, outline=imaging.ACCENT, width=4)
        imaging.label_chip_at(out, pts[0][0] + 6, pts[0][1] + 6, str(p["order"]))
    return out
