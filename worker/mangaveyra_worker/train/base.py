"""Shared training types and dataset preparation."""

from __future__ import annotations

import abc
import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable

from PIL import Image, ImageOps


@dataclass
class TrainRequest:
    """Everything a trainer needs.

    ``progress(step, steps, message)`` must be called regularly: it reports to the server and raises
    :class:`~mangaveyra_worker.errors.JobCanceled` when the job was canceled. A negative ``step`` only checks for
    cancellation (used while waiting on an external process).
    """

    trigger: str
    images: list[Image.Image]
    steps: int
    rank: int
    base_model: str
    base_path: str
    work_dir: Path
    resolution: int = 1024
    learning_rate: float = 1e-4
    seed: int = 0
    captions: list[str] = field(default_factory=list)
    progress: Callable[[int, int, str], None] = lambda step, steps, message: None

    def caption(self, i: int) -> str:
        """Caption for image ``i``: an explicit caption, else the trigger word plus a neutral tag."""
        if i < len(self.captions) and self.captions[i]:
            return self.captions[i]
        return f"{self.trigger}, 1character, solo"


class Trainer(abc.ABC):
    """Trains a LoRA and returns the path of the resulting ``.safetensors`` file."""

    name = ""

    @abc.abstractmethod
    def train(self, req: TrainRequest) -> Path:
        """Train and return the LoRA file path."""


def square_crop(img: Image.Image, size: int) -> Image.Image:
    """Fit the complete reference into an RGB square without losing identity details.

    Keep the historical helper name for external trainers. A centre crop of a
    tall turnaround discards the face and boots, and dropping alpha turns its
    transparent background black. Both built-in and external trainers use this
    preparation so the kept appearance remains the training target.
    """
    if size < 1:
        raise ValueError("training resolution must be positive")
    rgba = ImageOps.exif_transpose(img).convert("RGBA")
    white = Image.new("RGBA", rgba.size, "white")
    white.alpha_composite(rgba)
    return ImageOps.pad(
        white.convert("RGB"), (size, size), Image.Resampling.LANCZOS, color="white"
    )


def write_dataset(req: TrainRequest, repeats: int = 10) -> Path:
    """Write the reference images in the kohya-ss folder convention and return the image folder.

    ``<work>/dataset/<repeats>_<trigger>/0000.png`` + ``0000.txt`` (caption). Most trainers (kohya-ss, OneTrainer,
    ai-toolkit) accept this layout; ``metadata.jsonl`` is written too for diffusers-style ``imagefolder`` loaders.
    """
    safe_trigger = "".join(c if c.isalnum() or c in "-_" else "_" for c in req.trigger) or "character"
    folder = req.work_dir / "dataset" / f"{repeats}_{safe_trigger}"
    folder.mkdir(parents=True, exist_ok=True)
    lines = []
    for i, img in enumerate(req.images):
        name = f"{i:04d}.png"
        square_crop(img, req.resolution).save(folder / name)
        caption = req.caption(i)
        (folder / f"{i:04d}.txt").write_text(caption + "\n", encoding="utf-8")
        lines.append(json.dumps({"file_name": name, "text": caption}, ensure_ascii=False))
    (folder / "metadata.jsonl").write_text("\n".join(lines) + "\n", encoding="utf-8")
    return folder
