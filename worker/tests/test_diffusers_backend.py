"""Real diffusers runs on a tiny SDXL test pipeline (CPU).

Skipped unless torch + diffusers are installed *and* ``MV_TEST_DIFFUSERS=1`` is set, because the first run
downloads ``hf-internal-testing/tiny-stable-diffusion-xl-pipe`` (a few MB) from the Hugging Face Hub.
"""

from __future__ import annotations

import hashlib
import io
import os
from pathlib import Path
from typing import Any, Callable

import pytest
from PIL import Image, ImageDraw

pytest.importorskip("torch")
pytest.importorskip("diffusers")
if os.environ.get("MV_TEST_DIFFUSERS") != "1":
    pytest.skip("set MV_TEST_DIFFUSERS=1 to run the tiny-pipeline diffusers tests", allow_module_level=True)

from mangaveyra_worker.backends.diffusers_backend import DiffusersBackend  # noqa: E402
from mangaveyra_worker.config import Config  # noqa: E402
from tests.conftest import png  # noqa: E402

TINY = """
id: tiny-sdxl
name: Tiny SDXL (test)
kind: image
vram_mb: 100
worker:
  diffusers:
    repo: hf-internal-testing/tiny-stable-diffusion-xl-pipe
    family: sdxl
    dtype: float32
    default_steps: 3
    default_guidance: 5.0
    default_size: [64, 64]
    trainable: true
    train_resolution: 64
---
id: tiny-sdxl-pose
vram_mb: 100
worker:
  diffusers:
    repo: hf-internal-testing/tiny-stable-diffusion-xl-pipe
    family: sdxl
    dtype: float32
    default_steps: 2
    default_size: [64, 64]
    controlnet_openpose: hf-internal-testing/tiny-controlnet-sdxl
    job_types: [image.generate]
"""


@pytest.fixture(scope="module")
def backend(tmp_path_factory: pytest.TempPathFactory) -> DiffusersBackend:
    models = tmp_path_factory.mktemp("models")
    for i, doc in enumerate(TINY.split("---")):
        (models / f"tiny{i}.yaml").write_text(doc)
    data = tmp_path_factory.mktemp("data")
    b = DiffusersBackend(Config(device="cpu", models_dir=models, data_dir=data, embedder="structural"), [])
    yield b
    b.close()


def _img(client: Any, key: str) -> Image.Image:
    return Image.open(io.BytesIO(client.uploads[key]["data"]))


def test_advertises_tiny_model(backend: DiffusersBackend) -> None:
    assert backend.models() == ["tiny-sdxl", "tiny-sdxl-pose"]
    assert set(backend.job_types()) == {
        "image.generate",
        "image.edit",
        "image.inpaint",
        "sketch.ink",
        "character.sheet",
        "character.train",
    }


def test_generate_is_deterministic_with_progress(
    backend: DiffusersBackend, run_job: Callable[..., Any]
) -> None:
    inp = {"prompt": "Rin Asagiri", "n": 2, "seed": 11, "width": 64, "height": 64, "steps": 3}
    a, ca = run_job(backend, "image.generate", inp, model="tiny-sdxl")
    b, cb = run_job(backend, "image.generate", inp, model="tiny-sdxl")
    keys_a = [i["key"] for i in a.output["images"]]
    assert [i["seed"] for i in a.output["images"]] == [11, 12]
    assert [_img(ca, k).size for k in keys_a] == [(64, 64)] * 2

    def digest(c: Any, r: Any) -> list[str]:
        return [hashlib.sha256(c.uploads[i["key"]]["data"]).hexdigest() for i in r.output["images"]]

    assert digest(ca, a) == digest(cb, b)
    assert digest(ca, a)[0] != digest(ca, a)[1]
    steps = [c["step"] for c in ca.progress_calls if c.get("message") == "denoising"]
    assert steps == [1, 2, 3, 4, 5, 6]


def test_edit_inpaint_sketch_sheet(backend: DiffusersBackend, run_job: Callable[..., Any]) -> None:
    src = Image.new("RGB", (72, 64), (200, 200, 200))
    r, c = run_job(
        backend,
        "image.edit",
        {"prompt": "rain", "strength": 0.7, "seed": 1, "steps": 3},
        {"image": png(src)},
        model="tiny-sdxl",
    )
    assert _img(c, r.output["images"][0]["key"]).size == (72, 64)
    mask = Image.new("L", (72, 64), 0)
    ImageDraw.Draw(mask).rectangle((20, 20, 50, 50), fill=255)
    r, c = run_job(
        backend,
        "image.inpaint",
        {"prompt": "gull", "seed": 1, "steps": 3, "feather_px": 0},
        {"image": png(src), "mask": png(mask)},
        model="tiny-sdxl",
    )
    out = _img(c, r.output["images"][0]["key"]).convert("RGB")
    assert out.size == (72, 64) and out.getpixel((2, 2)) == (200, 200, 200)  # outside the mask untouched
    r, c = run_job(
        backend,
        "sketch.ink",
        {"strength": 0.5, "seed": 1, "steps": 3},
        {"sketch": png(src)},
        model="tiny-sdxl",
    )
    assert _img(c, r.output["images"][0]["key"]).size == (72, 64)
    r, c = run_job(
        backend, "character.sheet", {"prompt": "Sora Vell", "seed": 1, "steps": 2}, model="tiny-sdxl"
    )
    w, h = _img(c, r.output["images"][0]["key"]).size
    assert w > h


def test_train_lora_then_use_it(
    backend: DiffusersBackend, run_job: Callable[..., Any], tmp_path: Path
) -> None:
    refs = {f"ref_{i}": png(Image.new("RGB", (64, 64), (40 * i, 90, 160))) for i in range(3)}
    r, c = run_job(
        backend,
        "character.train",
        {"trigger": "kaitombeki", "steps": 3, "rank": 4, "seed": 5},
        refs,
        model="tiny-sdxl",
    )
    out = r.output
    lora = c.uploads[out["lora"]["key"]]
    assert lora["filename"] == "kaitombeki.safetensors" and len(lora["data"]) > 100
    assert len(out["test_images"]) == 2 and 0.0 <= out["score"] <= 1.0
    # Use the trained LoRA in a generation job through MV_LORA_DIR.
    (tmp_path / "kaito.safetensors").write_bytes(lora["data"])
    backend.config.lora_dir = tmp_path
    r, c = run_job(
        backend,
        "image.generate",
        {
            "prompt": "kaitombeki",
            "seed": 2,
            "steps": 2,
            "loras": [{"key": "kaito.safetensors", "weight": 0.8}],
        },
        model="tiny-sdxl",
    )
    assert len(r.output["images"]) == 1


def test_cancel_mid_denoise(backend: DiffusersBackend, run_job: Callable[..., Any]) -> None:
    from mangaveyra_worker.errors import JobCanceled

    with pytest.raises(JobCanceled):
        run_job(
            backend, "image.generate", {"seed": 1, "steps": 10, "n": 2}, model="tiny-sdxl", cancel_after=3
        )


def test_pose_switches_to_openpose_controlnet(backend: DiffusersBackend, run_job: Callable[..., Any]) -> None:
    pose = {"keypoints": [[0.5, 0.1], [0.5, 0.3], [0.4, 0.3], [0.35, 0.5]]}
    r, c = run_job(backend, "image.generate", {"seed": 1, "steps": 2, "pose": pose}, model="tiny-sdxl-pose")
    assert _img(c, r.output["images"][0]["key"]).size == (64, 64)
    entry = backend.cache._entries["tiny-sdxl-pose:base"]
    assert "text2img:hf-internal-testing/tiny-controlnet-sdxl" in entry.derived
