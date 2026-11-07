"""Offline FashionCLIP garment features; no downloads or repository code."""
from __future__ import annotations

import hashlib
import importlib.util
from pathlib import Path
import threading
from typing import Sequence

from PIL import Image, ImageOps


def fashion_available(model_dir: str | Path | None) -> bool:
    if not model_dir or not importlib.util.find_spec("torch") or not importlib.util.find_spec("transformers"):
        return False
    return all((Path(model_dir) / f).is_file() for f in (
        "config.json", "preprocessor_config.json", "model.safetensors"
    ))


class FashionEmbedder:
    name = "fashion"
    _lock = threading.Lock()
    _cache: dict[str, tuple[object, object, str]] = {}

    def __init__(self, model_dir: str | Path | None) -> None:
        if not fashion_available(model_dir):
            raise RuntimeError("FashionCLIP is not provisioned locally")
        self.path = Path(model_dir).resolve()
        self.method = ""
        self.config_sha256 = hashlib.sha256((self.path / "config.json").read_bytes()).hexdigest()
        self.processor_sha256 = hashlib.sha256((self.path / "preprocessor_config.json").read_bytes()).hexdigest()

    def _load(self) -> tuple[object, object]:
        from transformers import CLIPImageProcessor, CLIPModel

        key = str(self.path)
        with self._lock:
            if key not in self._cache:
                digest = hashlib.sha256()
                with (self.path / "model.safetensors").open("rb") as f:
                    for chunk in iter(lambda: f.read(1024 * 1024), b""):
                        digest.update(chunk)
                processor = CLIPImageProcessor.from_pretrained(key, local_files_only=True)
                model = CLIPModel.from_pretrained(
                    key, local_files_only=True, trust_remote_code=False, use_safetensors=True
                ).eval().to("cpu")
                self._cache[key] = model, processor, digest.hexdigest()
            model, processor, sha = self._cache[key]
        self.method = "fashion-clip-v1:" + sha
        return model, processor

    def embed(self, images: Sequence[Image.Image]) -> list[list[float]]:
        import torch

        model, processor = self._load()
        out = []
        for start in range(0, len(images), 4):
            rgb = []
            for image in images[start:start + 4]:
                rgba = ImageOps.exif_transpose(image).convert("RGBA")
                white = Image.new("RGBA", rgba.size, "white")
                white.alpha_composite(rgba)
                rgb.append(white.convert("RGB"))
            inputs = processor(images=rgb, return_tensors="pt")
            with torch.inference_mode():
                features = model.get_image_features(**inputs)
                # Transformers 5 returns the projected pooler output; earlier
                # supported versions return the projected tensor directly.
                if not isinstance(features, torch.Tensor):
                    features = features.pooler_output
                if features.ndim != 2 or features.shape[1] != 512 or not torch.isfinite(features).all():
                    raise ValueError("invalid FashionCLIP features")
                features = torch.nn.functional.normalize(features, dim=-1)
            out.extend(features.float().cpu().tolist())
        return out
