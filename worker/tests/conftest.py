"""Shared fixtures."""

from __future__ import annotations

import io
import threading
from pathlib import Path
from typing import Any, Callable, Iterator

import pytest
from PIL import Image, ImageDraw

from mangaveyra_worker.backoff import Backoff
from mangaveyra_worker.client import ServerClient
from mangaveyra_worker.config import Config
from mangaveyra_worker.jobs import Job, JobContext
from tests.fakes import TOKEN, FakeComfyUI, FakeMangaVeyra


def png(img: Image.Image) -> bytes:
    """Encode an image as PNG bytes."""
    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return buf.getvalue()


def three_panel_page(width: int = 800, height: int = 1130) -> Image.Image:
    """A synthetic manga page: one wide panel on top, two panels side by side below, art inside each."""
    img = Image.new("RGB", (width, height), "white")
    d = ImageDraw.Draw(img)
    m, g = int(width * 0.05), int(width * 0.05)
    top_h = int(height * 0.38)
    rects = [
        (m, m, width - m, m + top_h),  # top, wide
        (width // 2 + g // 2, m + top_h + g, width - m, height - m),  # bottom right
        (m, m + top_h + g, width // 2 - g // 2, height - m),  # bottom left
    ]
    for i, r in enumerate(rects):
        d.rectangle(r, outline="black", width=5)
        cx, cy = (r[0] + r[2]) // 2, (r[1] + r[3]) // 2
        d.ellipse((cx - 50, cy - 70, cx + 50, cy + 30), fill=(70, 70, 70))  # head-ish blob
        d.line((r[0] + 20, r[3] - 30, r[2] - 20, r[1] + 40 + 10 * i), fill="black", width=3)  # speed line
    d.text((width // 2 - 5, height - m // 2 - 5), "7", fill="black")  # page number in the margin
    return img


@pytest.fixture
def fake_server() -> Iterator[FakeMangaVeyra]:
    """A running fake MangaVeyra server."""
    server = FakeMangaVeyra().start()
    yield server
    server.stop()


@pytest.fixture
def fake_comfy() -> Iterator[FakeComfyUI]:
    """A running fake ComfyUI."""
    comfy = FakeComfyUI().start()
    yield comfy
    comfy.stop()


@pytest.fixture
def make_config(tmp_path: Path) -> Callable[..., Config]:
    """Build a test config (mock backend, no step delay, short long-polls)."""

    def factory(**overrides: Any) -> Config:
        values: dict[str, Any] = {
            "server": "http://127.0.0.1:9",
            "token": TOKEN,
            "name": "test-worker",
            "backends": ["mock"],
            "data_dir": tmp_path / "data",
            "mock_step_delay": 0.0,
            "claim_wait_s": 1,
            "drain_timeout_s": 5.0,
            "log_format": "text",
        }
        values.update(overrides)
        return Config.from_mapping(values)

    return factory


def fast_client(server_url: str, **kw: Any) -> ServerClient:
    """A protocol client with near-zero backoff for tests."""
    return ServerClient(
        server_url, TOKEN, backoff_factory=lambda: Backoff(base=0.01, cap=0.05, jitter=0.0), **kw
    )


class RecordingClient:
    """In-memory stand-in for ServerClient used by backend unit tests."""

    def __init__(self, files: dict[str, bytes] | None = None, cancel_after: int | None = None) -> None:
        self.files = files or {}
        self.uploads: dict[str, dict[str, Any]] = {}
        self.progress_calls: list[dict[str, Any]] = []
        self.cancel_after = cancel_after
        self.abort = threading.Event()

    def progress(self, job_id: str, progress: float, **kw: Any) -> bool:
        self.progress_calls.append({"progress": progress, **kw})
        return self.cancel_after is not None and len(self.progress_calls) >= self.cancel_after

    def upload_artifact(self, job_id: str, name: str, data: bytes, **kw: Any) -> str:
        key = f"jobs/{job_id}/{name}"
        self.uploads[key] = {"data": data, **kw}
        return key

    def download(self, url: str) -> bytes:
        return self.files[url]


@pytest.fixture
def run_job(tmp_path: Path) -> Callable[..., tuple[Any, RecordingClient]]:
    """Run one job on a backend with an in-memory client: ``run_job(backend, type, input, files)``."""

    def runner(
        backend: Any,
        job_type: str,
        inp: dict[str, Any] | None = None,
        files: dict[str, bytes] | None = None,
        model: str | None = None,
        cancel_after: int | None = None,
    ) -> tuple[Any, RecordingClient]:
        client = RecordingClient(files, cancel_after)
        job = Job.from_payload(
            {
                "id": "01JTESTJOB",
                "type": job_type,
                "input": inp or {},
                "files": {k: k for k in (files or {})},
                "model": {"id": model} if model else None,
            }
        )
        ctx = JobContext(job, client, scratch_root=tmp_path / "scratch", progress_interval=0.0)  # type: ignore[arg-type]
        try:
            return backend.run(job, ctx), client
        finally:
            ctx.cleanup()

    return runner
