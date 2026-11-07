"""CLI and backend registry."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from PIL import Image

from mangaveyra_worker.backends import build_backends
from mangaveyra_worker.cli import main
from mangaveyra_worker.config import Config
from mangaveyra_worker.errors import BackendUnavailable
from tests.conftest import three_panel_page


def test_check_prints_capabilities(
    capsys: pytest.CaptureFixture[str], monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("MV_BACKENDS", "mock")
    assert main(["--log-format", "json", "check"]) == 0
    doc = json.loads(capsys.readouterr().out)
    assert doc["backends"][0]["backend"] == "mock"
    assert "character.train" in doc["job_types"]


def test_local_job(
    tmp_path: Path, capsys: pytest.CaptureFixture[str], monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("MV_MOCK_STEP_DELAY", "0")
    page = tmp_path / "page.png"
    three_panel_page().save(page)
    out = tmp_path / "out"
    code = main(
        [
            "--backends",
            "mock",
            "--log-format",
            "json",
            "job",
            "detect.panels",
            "--file",
            f"page={page}",
            "--input",
            '{"reading": "ltr"}',
            "--out",
            str(out),
        ]
    )
    assert code == 0
    doc = json.loads(capsys.readouterr().out)
    assert len(doc["output"]["panels"]) == 3
    code = main(
        [
            "--backends",
            "mock",
            "--log-format",
            "json",
            "job",
            "image.generate",
            "--input",
            '{"width": 96, "height": 64, "seed": 1}',
            "--out",
            str(out),
        ]
    )
    assert code == 0
    assert Image.open(out / "variation-1.png").size == (96, 64)


def test_run_requires_token(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("MV_WORKER_TOKEN", raising=False)
    assert main(["--backends", "mock", "--log-format", "json", "run"]) == 2


def test_bad_config_exit_code(capsys: pytest.CaptureFixture[str]) -> None:
    assert main(["--server", "not-a-url", "check"]) == 2
    assert "MV_SERVER" in capsys.readouterr().err


def test_auto_without_gpu_serves_full_mock(monkeypatch: pytest.MonkeyPatch) -> None:
    # An empty CUDA inventory does not exclude Apple MPS on a developer's Mac.
    # Make this no-accelerator scenario explicit on every installed test extra.
    monkeypatch.setattr(
        "mangaveyra_worker.backends.diffusers_backend._resolve_device", lambda setting, torch: "cpu"
    )
    backends = build_backends(Config(backends=["auto"], ocr_engine="none"), [])
    names = [b.name for b in backends]
    assert names[-1] == "mock"
    assert "image.generate" in backends[-1].job_types()
    assert "diffusers" not in names  # no torch here, or no GPU


def test_explicit_unavailable_backend_is_an_error() -> None:
    with pytest.raises(BackendUnavailable):
        build_backends(Config(backends=["comfyui"]), [])  # no MV_COMFYUI_URL


def test_auto_with_real_art_backend_keeps_mock_honest(monkeypatch: pytest.MonkeyPatch) -> None:
    from mangaveyra_worker import backends as registry
    from mangaveyra_worker.backends.base import Backend

    class FakeArt(Backend):
        name = "diffusers"

        def job_types(self) -> list[str]:
            return ["image.generate"]

        def run(self, job, ctx):  # type: ignore[no-untyped-def]
            raise NotImplementedError

    real = registry.backend_class
    monkeypatch.setattr(
        registry, "backend_class", lambda name: FakeArt if name == "diffusers" else real(name)
    )
    backends = build_backends(Config(backends=["auto"], ocr_engine="none"), [])
    mock = backends[-1]
    assert mock.name == "mock"
    assert set(mock.job_types()) <= {"detect.panels", "upscale"}
    assert "character.score" not in {t for b in backends if b.name == "mock" for t in b.job_types()}
