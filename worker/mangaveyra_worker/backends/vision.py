"""Vision helpers backend: panel/bubble detection, identity scoring and OCR with real models.

Scoring needs numpy and an explicitly provisioned learned model. Panel detection
additionally needs OpenCV. OCR is advertised only when an engine is configured
and available (see :mod:`mangaveyra_worker.vision.ocr`).
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
        """Scoring can run without the independent OpenCV detector."""
        try:
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
        """Advertise only installed capabilities; model readiness is separate."""
        import importlib.util

        types = ["character.score"]
        if importlib.util.find_spec("cv2"):
            types.insert(0, "detect.panels")
        if self._ocr is not None:
            types.append("ocr")
        return types

    def models(self) -> list[str]:
        from mangaveyra_worker.vision.embed import dino_available
        from mangaveyra_worker.vision.ccip import ccip_available
        if self.config.embedder == "ccip":
            if self.config.outfit_mode == "multiview":
                from mangaveyra_worker.vision.fashion import fashion_available
                from mangaveyra_worker.vision.multiview import MODEL, load_artifact

                if not self.config.outfit_model or not fashion_available(self.config.fashion_model_dir) or not ccip_available(self.config.ccip_model_dir):
                    return []
                try:
                    load_artifact(self.config.outfit_model)
                except (OSError, ValueError, TypeError, KeyError):
                    return []
                return [MODEL]
            if self.config.outfit_model or self.config.fashion_model_dir:
                from mangaveyra_worker.vision.fashion import fashion_available
                from mangaveyra_worker.vision.outfit import load_artifact

                if not ccip_available(self.config.ccip_model_dir) or not fashion_available(self.config.fashion_model_dir) or not self.config.outfit_model:
                    return []
                try:
                    load_artifact(self.config.outfit_model)
                except (OSError, ValueError, TypeError, KeyError):
                    return []
                return ["character-lock-ccip-outfit-v3"]
            return ["character-lock-ccip-v1"] if ccip_available(self.config.ccip_model_dir) else []
        return ["character-lock-dinov2-v1"] if dino_available(self.config.dino_model_dir) else []

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
        paired = job.model_id == "character-lock-ccip-outfit-v3"
        multiview = job.model_id == "character-lock-ccip-outfit-multiview-v1"
        if multiview and (self.config.outfit_mode != "multiview" or self.config.embedder != "ccip" or not self.config.ccip_model_dir or not self.config.fashion_model_dir or not self.config.outfit_model):
            raise BadInput("the explicit CCIP multiview profile is not provisioned")
        if paired and (self.config.embedder != "ccip" or not self.config.ccip_model_dir or not self.config.fashion_model_dir or not self.config.outfit_model):
            raise BadInput("the explicit CCIP/outfit profile is not provisioned")
        preference = {"character-lock-dinov2-v1": "dinov2", "character-lock-ccip-v1": "ccip", "character-lock-ccip-outfit-v3": "ccip", "character-lock-ccip-outfit-multiview-v1": "ccip"}.get(job.model_id, self.config.embedder)
        model_dir = self.config.ccip_model_dir if preference == "ccip" else self.config.dino_model_dir
        scores, method = score_identity(refs, cands, preference, model_dir)
        outfit_refs = ctx.images("outfit_")
        outfit_scores: list[float | None] = [None] * len(cands)
        if multiview:
            return self._multiview_outfit(cands, outfit_refs, scores, method)
        if paired:
            from mangaveyra_worker.vision.outfit import OutfitMatcher

            matcher = OutfitMatcher(self.config.ccip_model_dir, self.config.fashion_model_dir, self.config.outfit_model)
            if method != "ccip-caformer24-v1:" + matcher.artifact["ccip_sha256"]:
                raise BadInput("identity model differs from the frozen outfit feature model")
            features = [None] * len(cands)
            if outfit_refs:
                matching = [i for i, score in enumerate(scores) if score >= .9]
                if matching:
                    values, _, measured = matcher.score(outfit_refs, [cands[i] for i in matching])
                    for i, value, feature in zip(matching, values, measured, strict=True):
                        outfit_scores[i], features[i] = value, feature
            return {
                "scores": scores, "outfit_scores": outfit_scores, "method": matcher.method,
                "identity_method": method, "outfit_features": features,
                "feature_methods": {
                    "ccip": method,
                    "fashion": "fashion-clip-v1:" + matcher.artifact["fashion_sha256"],
                },
            }
        if outfit_refs:
            def body(im: Any) -> Any:
                return im.crop((0, int(im.height * 0.35), im.width, im.height))
            values, outfit_method = score_identity([body(im) for im in outfit_refs], [body(im) for im in cands], preference, model_dir)
            if outfit_method != method:
                raise BadInput("identity and outfit methods must match")
            outfit_scores = list(values)
        return {"scores": scores, "outfit_scores": outfit_scores, "method": method}

    def _multiview_outfit(self, candidates, references, identities, identity_method):
        from mangaveyra_worker.vision.multiview import MultiviewMatcher, REFERENCE_LIMIT

        if len(references) > REFERENCE_LIMIT or not identity_method.startswith("ccip-caformer24-v1:"):
            raise BadInput("invalid multiview reference/model contract")
        matcher = MultiviewMatcher(self.config.ccip_model_dir, self.config.fashion_model_dir, self.config.outfit_model)
        if identity_method != "ccip-caformer24-v1:" + matcher.pair["ccip_sha256"]:
            raise BadInput("multiview identity model differs from the frozen clothing features")
        outfits, indices, measured = [None] * len(candidates), [None] * len(candidates), [None] * len(candidates)
        pair_indices, features, pairs = [None] * len(candidates), [None] * len(candidates), [None] * len(candidates)
        matching = [i for i, value in enumerate(identities) if value >= .9]
        if references and matching:
            values, feature_rows, pair_rows = matcher.score(references, [candidates[i] for i in matching])
            for i, value, row, pair_row in zip(matching, values, feature_rows, pair_rows, strict=True):
                cosines = [feature[0] for feature in row]
                selected = max(range(len(cosines)), key=cosines.__getitem__)
                outfits[i], indices[i], measured[i] = value, selected, cosines
                features[i], pairs[i] = row, pair_row
                pair_indices[i] = max(range(len(pair_row)), key=pair_row.__getitem__)
        return {
            "scores": identities, "outfit_scores": outfits,
            "method": matcher.method,
            "identity_method": identity_method, "outfit_reference_count": len(references),
            "outfit_reference_indices": indices, "outfit_cosines": measured,
            "outfit_pair_reference_indices": pair_indices, "outfit_pair_features": features,
            "outfit_pair_scores": pairs,
            "outfit_fit": {key: matcher.pair[key] for key in ("mean", "scale", "weights", "intercept")},
            "feature_methods": {"ccip": identity_method, "fashion": "fashion-clip-v1:" + matcher.pair["fashion_sha256"]},
        }

    def _ocr_job(self, job: Job, ctx: JobContext) -> dict[str, Any]:
        assert self._ocr is not None
        lang = str(job.input.get("lang") or "ja")
        img = ctx.image("image")
        ctx.progress(0.2, message="reading text", force=True)
        return {"lines": self._ocr.recognise(img, lang), "engine": self._ocr.name}
