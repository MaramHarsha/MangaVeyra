"""Identity embeddings for ``character.score`` (Character Lock).

The score of a candidate is the cosine similarity between its embedding and the centroid of the reference
embeddings, clamped to 0–1. Embedders, best first:

* ``insightface`` — ArcFace face-identity embeddings (``pip install insightface onnxruntime``; the ``buffalo_l``
  pack downloads on first use). Used only when *every* image has a detectable face; stylised manga faces often
  defeat real-face detectors, in which case the next embedder is used for the whole job.
* ``clip`` — OpenCLIP image embeddings (``pip install open_clip_torch``), robust on drawn characters.
* ``structural`` — a dependency-light descriptor (numpy only): gradient-orientation histograms, a tonal
  histogram and a coarse thumbnail. It is a weak identity signal, but always available and deterministic.

Embeddings are computed on the whole image; the server sends character crops (panel layers), not whole pages.
"""

from __future__ import annotations

import logging
import math
import threading
from typing import Protocol, Sequence

from PIL import Image, ImageOps

log = logging.getLogger(__name__)


class Embedder(Protocol):
    """Maps images to L2-normalised vectors (``None`` when the image has no usable subject, e.g. no face)."""

    name: str

    def embed(self, images: Sequence[Image.Image]) -> list[list[float] | None]:
        """Embed each image."""


def _normalise(vec: Sequence[float]) -> list[float]:
    n = math.sqrt(sum(v * v for v in vec)) or 1.0
    return [v / n for v in vec]


def cosine(a: Sequence[float], b: Sequence[float]) -> float:
    """Cosine similarity of two vectors."""
    na = math.sqrt(sum(x * x for x in a)) or 1.0
    nb = math.sqrt(sum(x * x for x in b)) or 1.0
    return sum(x * y for x, y in zip(a, b, strict=True)) / (na * nb)


def centroid(vectors: Sequence[Sequence[float]]) -> list[float]:
    """Normalised mean of normalised vectors."""
    if not vectors:
        raise ValueError("no vectors")
    dim = len(vectors[0])
    acc = [0.0] * dim
    for v in vectors:
        for i, x in enumerate(_normalise(v)):
            acc[i] += x
    return _normalise(acc)


class StructuralEmbedder:
    """Gradient-orientation + tone + thumbnail descriptor (numpy). Mean-centred per block so unrelated images
    score noticeably lower than near-duplicates."""

    name = "structural"

    def __init__(self, size: int = 96, cells: int = 4, bins: int = 9) -> None:
        import numpy as np  # noqa: F401 - fail early when numpy is missing

        self.size, self.cells, self.bins = size, cells, bins

    def _one(self, img: Image.Image) -> list[float]:
        import numpy as np

        g = np.asarray(
            ImageOps.grayscale(img).resize((self.size, self.size), Image.Resampling.BILINEAR),
            dtype=np.float32,
        )
        g /= 255.0
        gx = np.zeros_like(g)
        gy = np.zeros_like(g)
        gx[:, 1:-1] = g[:, 2:] - g[:, :-2]
        gy[1:-1, :] = g[2:, :] - g[:-2, :]
        mag = np.hypot(gx, gy)
        ang = (np.arctan2(gy, gx) % np.pi) / np.pi * self.bins
        idx = np.minimum(ang.astype(np.int32), self.bins - 1)
        step = self.size // self.cells
        hog = []
        for cy in range(self.cells):
            for cx in range(self.cells):
                sl = (slice(cy * step, (cy + 1) * step), slice(cx * step, (cx + 1) * step))
                hist = np.bincount(idx[sl].ravel(), weights=mag[sl].ravel(), minlength=self.bins)
                hog.append(hist / (np.linalg.norm(hist) + 1e-6))
        hog_v = np.concatenate(hog)
        tone = np.histogram(g, bins=16, range=(0.0, 1.0))[0].astype(np.float32)
        tone /= tone.sum() + 1e-6
        thumb = (
            np.asarray(
                ImageOps.grayscale(img).resize((12, 12), Image.Resampling.BOX), dtype=np.float32
            ).ravel()
            / 255.0
        )
        parts = []
        for block, weight in ((hog_v, 1.0), (tone, 0.6), (thumb, 0.8)):
            b = block - block.mean()
            parts.append(weight * b / (np.linalg.norm(b) + 1e-6))
        return _normalise(np.concatenate(parts).tolist())

    def embed(self, images: Sequence[Image.Image]) -> list[list[float] | None]:
        """Embed each image."""
        return [self._one(im) for im in images]


class ClipEmbedder:
    """OpenCLIP image embeddings (``open_clip_torch``)."""

    name = "clip"
    _lock = threading.Lock()
    _cache: dict[str, tuple[object, object]] = {}

    def __init__(
        self, model: str = "ViT-B-32", pretrained: str = "laion2b_s34b_b79k", device: str | None = None
    ) -> None:
        import open_clip  # noqa: F401
        import torch

        self.model_name, self.pretrained = model, pretrained
        self.device = device or ("cuda" if torch.cuda.is_available() else "cpu")

    def _load(self) -> tuple[object, object]:
        import open_clip

        key = f"{self.model_name}/{self.pretrained}/{self.device}"
        with self._lock:
            if key not in self._cache:
                model, _, preprocess = open_clip.create_model_and_transforms(
                    self.model_name, pretrained=self.pretrained
                )
                model.eval().to(self.device)
                self._cache[key] = (model, preprocess)
            return self._cache[key]

    def embed(self, images: Sequence[Image.Image]) -> list[list[float] | None]:
        """Embed each image."""
        import torch

        model, preprocess = self._load()
        batch = torch.stack([preprocess(im.convert("RGB")) for im in images]).to(self.device)  # type: ignore[operator]
        with torch.no_grad():
            feats = model.encode_image(batch)  # type: ignore[attr-defined]
            feats = feats / feats.norm(dim=-1, keepdim=True)
        return [f.float().cpu().tolist() for f in feats]


class InsightFaceEmbedder:
    """ArcFace identity embeddings from InsightFace (largest face per image)."""

    name = "insightface"
    _lock = threading.Lock()
    _app: object | None = None

    def __init__(self, pack: str = "buffalo_l") -> None:
        import insightface  # noqa: F401

        self.pack = pack

    def _load(self) -> object:
        from insightface.app import FaceAnalysis

        with self._lock:
            if InsightFaceEmbedder._app is None:
                app = FaceAnalysis(
                    name=self.pack, providers=["CUDAExecutionProvider", "CPUExecutionProvider"]
                )
                app.prepare(ctx_id=0, det_size=(640, 640))
                InsightFaceEmbedder._app = app
        return InsightFaceEmbedder._app

    def embed(self, images: Sequence[Image.Image]) -> list[list[float] | None]:
        """Embed each image; ``None`` where no face is found."""
        import numpy as np

        app = self._load()
        out: list[list[float] | None] = []
        for im in images:
            bgr = np.asarray(im.convert("RGB"))[:, :, ::-1]
            faces = app.get(bgr)  # type: ignore[attr-defined]
            if not faces:
                out.append(None)
                continue
            face = max(faces, key=lambda f: (f.bbox[2] - f.bbox[0]) * (f.bbox[3] - f.bbox[1]))
            out.append(_normalise(face.normed_embedding.tolist()))
        return out


def available_embedders(preference: str = "auto") -> list[str]:
    """Embedder names usable on this machine, best first, filtered by ``preference``."""
    names: list[str] = []
    order = ["insightface", "clip", "structural"] if preference == "auto" else [preference]
    for name in order:
        try:
            if name == "insightface":
                import insightface  # noqa: F401
            elif name == "clip":
                import open_clip  # noqa: F401
                import torch  # noqa: F401
            elif name == "structural":
                import numpy  # noqa: F401
            names.append(name)
        except ImportError:
            continue
    return names


def make_embedder(name: str) -> Embedder:
    """Construct an embedder by name."""
    if name == "insightface":
        return InsightFaceEmbedder()
    if name == "clip":
        return ClipEmbedder()
    if name == "structural":
        return StructuralEmbedder()
    raise ValueError(f"unknown embedder {name!r}")


def score_identity(
    references: Sequence[Image.Image], candidates: Sequence[Image.Image], preference: str = "auto"
) -> tuple[list[float], str]:
    """Score each candidate against the references. Returns ``(scores, embedder_name)``.

    Tries embedders best-first; an embedder that cannot embed every image (no face found) is skipped.
    """
    if not references or not candidates:
        raise ValueError("need at least one reference and one candidate")
    last_error: Exception | None = None
    for name in available_embedders(preference):
        try:
            emb = make_embedder(name)
            vecs = emb.embed([*references, *candidates])
        except Exception as exc:  # model download failures etc. — try the next embedder
            log.warning(
                "embedder failed, trying the next one",
                extra={"fields": {"embedder": name, "error": str(exc)}},
            )
            last_error = exc
            continue
        if any(v is None for v in vecs):
            continue
        refs = [v for v in vecs[: len(references)] if v is not None]
        cands = vecs[len(references) :]
        c = centroid(refs)
        scores = [round(max(0.0, min(1.0, cosine(c, v))), 4) for v in cands if v is not None]
        return scores, name
    raise RuntimeError(f"no identity embedder could process the images ({last_error})")
