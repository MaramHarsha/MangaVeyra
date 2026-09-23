"""Vision helpers backend: panel/bubble detection, identity scoring and OCR with real models.

Needs the ``vision`` extra (``opencv-python-headless`` + ``numpy``). ``character.score`` uses the best available
identity embedder (InsightFace → OpenCLIP → structural descriptor) and ``ocr`` is only advertised when an OCR engine
is installed or configured (see :mod:`mangaveyra_worker.vision.ocr`).
"""

from __future__ import annotations

import time
from typing import Any

from mangaveyra_worker.backends.base import Backend
from mangaveyra_worker.errors import BadInput
from mangaveyra_worker.jobs import Job, JobContext, JobResult, clamp_float
from mangaveyra_worker.vision.ocr import OcrEngine, make_ocr_engine


class VisionBackend(Backend):
    """OpenCV panel detection, identity embeddings and pluggable OCR."""

    name = "vision"

    @classmethod
    def available(cls, config: Any) -> tuple[bool, str]:
        """Requires OpenCV and numpy."""
        try:
            import cv2  # noqa: F401
            import numpy  # noqa: F401
        except ImportError as exc:
            return False, f"{exc} (pip install 'mangaveyra-worker[vision]')"
        return True, ""

    def __init__(self, config: Any, gpus: list[dict[str, Any]]) -> None:
        super().__init__(config, gpus)
        self._ocr: OcrEngine | None = None
        self._ocr_error = ""
        if config.ocr_engine != "none":
            try:
                self._ocr = make_ocr_engine(config.ocr_engine, config.ocr_url)
            except Exception as exc:  # an explicitly configured engine that cannot load
                self._ocr_error = str(exc)

    def job_types(self) -> list[str]:
        """``detect.panels`` and ``character.score`` always; ``ocr`` when an engine is available."""
        types = ["detect.panels", "character.score"]
        if self._ocr is not None:
            types.append("ocr")
        return types

    def describe(self) -> dict[str, Any]:
        """Include the OCR engine in startup logs."""
        d = super().describe()
        d["ocr_engine"] = self._ocr.name if self._ocr else (self._ocr_error or "none")
        return d

    def run(self, job: Job, ctx: JobContext) -> JobResult:
        """Run one vision job (CPU time is not reported as GPU time unless an accelerator is used)."""
        started = time.monotonic()
        if job.type == "detect.panels":
            return JobResult(self._panels(job, ctx))
        if job.type == "character.score":
            out = self._score(job, ctx)
            gpu = (
                time.monotonic() - started
                if out.get("method") in ("insightface", "clip") and self.gpus
                else 0.0
            )
            return JobResult(out, gpu_seconds=gpu)
        if job.type == "ocr":
            return JobResult(self._ocr_job(job, ctx))
        raise BadInput(f"vision backend cannot run {job.type}")

    def _panels(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        from mangaveyra_worker.vision.panels import detect_bubbles, detect_panels

        reading = str(job.input.get("reading") or "rtl")
        if reading not in ("rtl", "ltr", "vertical"):
            raise BadInput("reading must be rtl, ltr or vertical")
        page = ctx.image("page")
        ctx.progress(0.2, message="detecting panels", force=True)
        panels = detect_panels(page, reading)
        ctx.progress(0.6, message="detecting bubbles")
        bubbles = detect_bubbles(page)
        if self._ocr is not None and bubbles and job.input.get("ocr", True):
            ctx.progress(0.8, message="reading bubble text")
            lang = str(job.input.get("lang") or "ja")
            for b in bubbles:
                xs = [p[0] for p in b["points"]]
                ys = [p[1] for p in b["points"]]
                crop = page.crop((min(xs), min(ys), max(xs), max(ys)))
                lines = self._ocr.recognise(crop, lang)
                b["text"] = " ".join(line["text"] for line in lines).strip()
        return {"panels": panels, "bubbles": bubbles}

    def _score(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        from mangaveyra_worker.vision.embed import score_identity

        clamp_float(
            job.input.get("threshold"), 0.8, 0.0, 1.0, "threshold"
        )  # validated; the server applies it
        refs = ctx.images("reference_")
        cands = ctx.images("candidate_")
        if not refs or not cands:
            raise BadInput("character.score needs reference_N and candidate_N files")
        ctx.progress(0.3, message="embedding", force=True)
        scores, method = score_identity(refs, cands, self.config.embedder)
        return {"scores": scores, "method": method}

    def _ocr_job(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        assert self._ocr is not None
        lang = str(job.input.get("lang") or "ja")
        img = ctx.image("image")
        ctx.progress(0.2, message="reading text", force=True)
        return {"lines": self._ocr.recognise(img, lang), "engine": self._ocr.name}
