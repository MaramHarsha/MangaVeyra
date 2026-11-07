"""Offline CCIP boundary tests; no weights are downloaded by the test suite."""
from __future__ import annotations

import hashlib
import os
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace

import numpy as np
from PIL import Image, ImageDraw
import pytest

from mangaveyra_worker.vision.ccip import CcipEmbedder, ccip_available, preprocess


def test_worker_disables_library_telemetry_before_optional_imports(tmp_path) -> None:
    env = {
        **os.environ,
        "PYTHONPATH": str(Path(__file__).resolve().parents[1]),
        "ORT_DISABLE_TELEMETRY": "0",
        "HF_HUB_DISABLE_TELEMETRY": "0",
    }
    code = (
        "import os,sys; import mangaveyra_worker; "
        "assert os.environ['ORT_DISABLE_TELEMETRY']=='1'; "
        "assert os.environ['HF_HUB_DISABLE_TELEMETRY']=='1'; "
        "assert 'onnxruntime' not in sys.modules; assert 'transformers' not in sys.modules"
    )
    result = subprocess.run(
        [sys.executable, "-c", code], cwd=tmp_path, env=env, capture_output=True, text=True, timeout=10
    )
    assert result.returncode == 0, result.stderr


def test_ccip_preserves_the_full_character_and_flattens_alpha() -> None:
    image = Image.new("RGBA", (40, 160), (255, 0, 0, 0))
    draw = ImageDraw.Draw(image)
    draw.rectangle((0, 0, 39, 19), fill="blue")
    draw.rectangle((0, 140, 39, 159), fill="green")
    pixels = preprocess(image)
    assert pixels.shape == (3, 384, 384)
    mean = np.asarray((.48145466, .4578275, .40821073))
    std = np.asarray((.26862954, .26130258, .27577711))
    assert pixels[:, 0, 190] * std + mean == pytest.approx((0, 0, 1), abs=1e-6)
    assert pixels[:, 190, 190] * std + mean == pytest.approx((1, 1, 1), abs=1e-6)
    assert pixels[:, -1, 190] * std + mean == pytest.approx((0, 128 / 255, 0), abs=1e-6)
    with pytest.raises(ValueError, match="no visible"):
        preprocess(Image.new("RGBA", (40, 160), (0, 0, 0, 0)))


def test_ccip_requires_local_weights_and_disables_telemetry_before_session(tmp_path, monkeypatch) -> None:
    from mangaveyra_worker.vision import ccip
    monkeypatch.setattr(ccip.importlib.util, "find_spec", lambda name: object())
    assert not ccip_available(None)
    assert not ccip_available(tmp_path)
    weights = b"controlled feature graph fixture"
    (tmp_path / "model_feat.onnx").write_bytes(weights)
    calls = []

    class Session:
        def __init__(self, path, options, providers):
            assert calls == ["telemetry-disabled"]
            assert path == str((tmp_path / "model_feat.onnx").resolve())
            assert providers == ["CPUExecutionProvider"]
            calls.append("session")

        def get_inputs(self):
            return [SimpleNamespace(name="input", shape=["batch", 3, 384, 384])]

        def get_outputs(self):
            return [SimpleNamespace(name="output")]

        def run(self, names, inputs):
            assert inputs["input"].shape == (1, 3, 384, 384)
            return [np.ones((1, 768), dtype=np.float32)]

    monkeypatch.setitem(sys.modules, "onnxruntime", SimpleNamespace(
        disable_telemetry_events=lambda: calls.append("telemetry-disabled"),
        SessionOptions=SimpleNamespace,
        InferenceSession=Session,
    ))
    embedder = CcipEmbedder(tmp_path)
    vector = embedder.embed([Image.new("RGB", (40, 160), "black")])[0]
    assert np.linalg.norm(vector) == pytest.approx(1)
    assert embedder.method == "ccip-caformer24-v1:" + hashlib.sha256(weights).hexdigest()
