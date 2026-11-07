"""Explicitly provisioned, offline CCIP anime-character features.

The official caformer-24-randaug-pruned ONNX export is tagged OpenRAIL,
not Apache-2.0. No weights ship with the worker and no runtime download is
allowed. See the source/checksums and validation evidence before selecting it.
"""
from __future__ import annotations

import hashlib
import importlib.util
from pathlib import Path
import threading
from typing import Sequence

from PIL import Image, ImageChops, ImageOps


def ccip_available(model_dir: str | Path | None) -> bool:
    if not model_dir or not importlib.util.find_spec("onnxruntime"):
        return False
    return (Path(model_dir) / "model_feat.onnx").is_file()


def preprocess(image: Image.Image) -> object:
    """Use the published CCIP 384px full-image transform; retain all views."""
    import numpy as np

    rgba = ImageOps.exif_transpose(image).convert("RGBA")
    white = Image.new("RGBA", rgba.size, "white")
    white.alpha_composite(rgba)
    rgb = white.convert("RGB")
    foreground = ImageChops.difference(rgb, Image.new("RGB", rgb.size, "white")).convert("L")
    box = foreground.point(lambda n: 255 if n > 12 else 0).getbbox()
    if box is None or (box[2] - box[0]) * (box[3] - box[1]) < 16:
        raise ValueError("no visible character in scoring image")
    rgb = rgb.resize((384, 384), Image.Resampling.BILINEAR)
    data = np.asarray(rgb, dtype=np.float32).transpose(2, 0, 1) / 255.0
    mean = np.asarray((.48145466, .4578275, .40821073), dtype=np.float32)[:, None, None]
    std = np.asarray((.26862954, .26130258, .27577711), dtype=np.float32)[:, None, None]
    return (data - mean) / std


class CcipEmbedder:
    name = "ccip"
    _lock = threading.Lock()
    _cache: dict[str, tuple[object, str]] = {}

    def __init__(self, model_dir: str | Path | None) -> None:
        if not ccip_available(model_dir):
            raise RuntimeError("CCIP is not provisioned locally")
        self.path = (Path(model_dir) / "model_feat.onnx").resolve()
        self.method = ""

    def _load(self) -> object:
        import onnxruntime as ort

        # ORT's default telemetry is incompatible with the product contract.
        # Disable it before constructing any inference session.
        ort.disable_telemetry_events()
        with self._lock:
            key = str(self.path)
            if key not in self._cache:
                digest = hashlib.sha256(self.path.read_bytes()).hexdigest()
                options = ort.SessionOptions()
                options.intra_op_num_threads = 2
                options.inter_op_num_threads = 1
                options.log_severity_level = 3
                session = ort.InferenceSession(key, options, providers=["CPUExecutionProvider"])
                inputs, outputs = session.get_inputs(), session.get_outputs()
                if len(inputs) != 1 or inputs[0].shape[1:] != [3, 384, 384] or len(outputs) != 1:
                    raise ValueError("expected the official CCIP caformer-24 feature graph")
                self._cache[key] = (session, digest)
            session, digest = self._cache[key]
        self.method = "ccip-caformer24-v1:" + digest
        return session

    def embed(self, images: Sequence[Image.Image]) -> list[list[float] | None]:
        import numpy as np

        session = self._load()
        output = []
        # A single-image batch bounds CPU memory for large scoring projects.
        for image in images:
            features = session.run(None, {session.get_inputs()[0].name: preprocess(image)[None]})[0]
            if features.shape != (1, 768) or not np.isfinite(features).all():
                raise ValueError("invalid CCIP feature output")
            norm = np.linalg.norm(features[0])
            if norm <= 0:
                raise ValueError("empty CCIP features")
            output.append((features[0] / norm).tolist())
        return output
