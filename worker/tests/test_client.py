"""Protocol client against the fake server."""

from __future__ import annotations

import json
import socket
from collections import deque

import pytest

from mangaveyra_worker.client import ServerClient
from mangaveyra_worker.errors import ApiError, BadInput, Unauthorized, UnknownWorker
from tests.conftest import fast_client
from tests.fakes import FakeMangaVeyra


def test_register_heartbeat_claim_roundtrip(fake_server: FakeMangaVeyra) -> None:
    c = fast_client(fake_server.url)
    reg = c.register({"name": "w", "job_types": ["image.generate"], "models": ["mock"]})
    assert reg["worker_id"] == "01JWORKER"
    assert c.heartbeat("01JWORKER", [], []) == []
    assert c.claim("01JWORKER", ["image.generate"], ["mock"], wait_s=0) is None  # 204
    fake_server.enqueue({"id": "j1", "type": "image.generate"})
    job = c.claim("01JWORKER", ["image.generate"], ["mock"], wait_s=1)
    assert job is not None and job["id"] == "j1"
    assert fake_server.claims[-1] == {
        "worker_id": "01JWORKER",
        "job_types": ["image.generate"],
        "models": ["mock"],
        "wait_s": 1,
    }


def test_claim_skips_types_not_requested(fake_server: FakeMangaVeyra) -> None:
    c = fast_client(fake_server.url)
    fake_server.enqueue({"id": "v1", "type": "video.animate"})
    assert c.claim("w", ["image.generate"], [], wait_s=0) is None


def test_progress_upload_complete_fail(fake_server: FakeMangaVeyra) -> None:
    rec = fake_server.enqueue({"id": "j2", "type": "image.generate"})
    c = fast_client(fake_server.url)
    assert c.progress("j2", 0.5, step=10, steps=20, message="denoising") is False
    assert rec.progress[-1] == {"progress": 0.5, "step": 10, "steps": 20, "message": "denoising"}
    key = c.upload_artifact("j2", "variation-1", b"\x89PNG fake", meta={"seed": 3})
    assert key == "jobs/j2/variation-1"
    assert rec.artifacts[key]["data"] == b"\x89PNG fake"
    assert rec.artifacts[key]["meta"] == {"seed": 3}
    c.complete("j2", {"images": [{"key": key, "seed": 3}]}, {"gpu_seconds": 1.5})
    assert rec.result == {"output": {"images": [{"key": key, "seed": 3}]}, "actual": {"gpu_seconds": 1.5}}
    c.fail("j2", "oom", "out of memory", True)
    assert rec.error == {"code": "oom", "message": "out of memory", "retryable": True}


def test_progress_cancel_flag(fake_server: FakeMangaVeyra) -> None:
    fake_server.enqueue({"id": "j3", "type": "image.generate"})
    fake_server.cancel_after_progress["j3"] = 2
    c = fast_client(fake_server.url)
    assert c.progress("j3", 0.1) is False
    assert c.progress("j3", 0.2) is True


def test_progress_on_unknown_job_means_stop(fake_server: FakeMangaVeyra) -> None:
    assert fast_client(fake_server.url).progress("gone", 0.3) is True


def test_retries_server_errors_then_succeeds(fake_server: FakeMangaVeyra) -> None:
    fake_server.enqueue({"id": "j4", "type": "image.generate"})
    fake_server.faults["/jobs/*/complete"] = deque([503, 502, 500])
    c = fast_client(fake_server.url)
    c.complete("j4", {"images": []}, {"gpu_seconds": 0})
    assert fake_server.jobs["j4"].result is not None
    assert sum(1 for m, p in fake_server.requests if p.endswith("/j4/complete")) == 4


def test_gives_up_after_attempts(fake_server: FakeMangaVeyra) -> None:
    fake_server.faults["/heartbeat"] = deque([503] * 5)
    with pytest.raises(ApiError) as err:
        fast_client(fake_server.url).heartbeat("w", [], [])
    assert err.value.status == 503


def test_client_errors_are_not_retried(fake_server: FakeMangaVeyra) -> None:
    fake_server.faults["/register"] = deque([400])
    with pytest.raises(ApiError) as err:
        fast_client(fake_server.url).register({"name": "w"})
    assert err.value.status == 400
    assert sum(1 for _, p in fake_server.requests if p.endswith("/register")) == 1


def test_bad_token(fake_server: FakeMangaVeyra) -> None:
    c = ServerClient(fake_server.url, "mvw_wrong")
    with pytest.raises(Unauthorized):
        c.register({"name": "w"})


def test_unknown_worker_is_typed(fake_server: FakeMangaVeyra) -> None:
    fake_server.unknown_worker_once = True
    with pytest.raises(UnknownWorker):
        fast_client(fake_server.url).claim("w", ["ocr"], [], 0)


def test_network_error_becomes_api_error() -> None:
    with socket.socket() as s:  # a port nobody listens on
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    c = fast_client(f"http://127.0.0.1:{port}", timeout=1.0)
    with pytest.raises(ApiError) as err:
        c.heartbeat("w", [], [])
    assert err.value.code == "network"


def test_download_signed_and_relative_urls(fake_server: FakeMangaVeyra) -> None:
    url = fake_server.add_media("page.png", b"abc")
    c = fast_client(fake_server.url)
    assert c.download(url) == b"abc"
    assert c.download("/media/page.png?sig=x") == b"abc"
    with pytest.raises(BadInput):
        c.download("/media/missing.png")


def test_download_size_limit(fake_server: FakeMangaVeyra) -> None:
    url = fake_server.add_media("big.bin", b"x" * 5000)
    c = fast_client(fake_server.url, max_download_bytes=1000)
    with pytest.raises(BadInput, match="too large"):
        c.download(url)


def test_upload_meta_is_json_string(fake_server: FakeMangaVeyra) -> None:
    rec = fake_server.enqueue({"id": "j5", "type": "ocr"})
    fast_client(fake_server.url).upload_artifact(
        "j5",
        "lora",
        b"x",
        content_type="application/octet-stream",
        filename="rin.safetensors",
        meta={"a": [1, 2]},
    )
    art = rec.artifacts["jobs/j5/lora"]
    assert art["filename"] == "rin.safetensors"
    assert json.dumps(art["meta"]) == '{"a": [1, 2]}'
