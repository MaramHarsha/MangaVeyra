"""End-to-end worker flows against the fake server: claim → progress → artifacts → complete/fail."""

from __future__ import annotations

import threading
import time
from collections import deque
from typing import Any, Callable, Iterator

import pytest

from mangaveyra_worker.backends.base import Backend
from mangaveyra_worker.backends.mock import MockBackend
from mangaveyra_worker.backoff import Backoff
from mangaveyra_worker.config import Config
from mangaveyra_worker.errors import BadInput
from mangaveyra_worker.jobs import Job, JobContext, JobResult
from mangaveyra_worker.runner import Worker
from tests.conftest import fast_client
from tests.fakes import FakeMangaVeyra


class SleepyBackend(Backend):
    """Runs ``sleep.test`` jobs: sleeps ``seconds`` without reporting progress (for lease/cancel tests)."""

    name = "sleepy"
    active = 0
    peak = 0
    lock = threading.Lock()

    def job_types(self) -> list[str]:
        return ["upscale"]

    def run(self, job: Job, ctx: JobContext) -> JobResult:
        with SleepyBackend.lock:
            SleepyBackend.active += 1
            SleepyBackend.peak = max(SleepyBackend.peak, SleepyBackend.active)
        try:
            deadline = time.monotonic() + float(job.input.get("seconds", 1.0))
            while time.monotonic() < deadline:
                ctx.check()
                time.sleep(0.02)
            if job.input.get("explode"):
                raise RuntimeError("kaboom")
            if job.input.get("bad"):
                raise BadInput("nope")
            return JobResult({"images": []}, gpu_seconds=1.25)
        finally:
            with SleepyBackend.lock:
                SleepyBackend.active -= 1


@pytest.fixture
def start_worker(
    fake_server: FakeMangaVeyra, make_config: Callable[..., Config]
) -> Iterator[Callable[..., Worker]]:
    workers: list[tuple[Worker, threading.Thread]] = []
    results: dict[int, int] = {}

    def start(backends: list[Backend] | None = None, **cfg: Any) -> Worker:
        config = make_config(server=fake_server.url, **cfg)
        worker = Worker(
            config,
            backends or [MockBackend(config, [])],
            client=fast_client(fake_server.url),
            gpu_probe=lambda: [{"name": "Fake GPU", "vram_total_mb": 8192, "vram_free_mb": 8000}],
            claim_backoff=Backoff(base=0.01, cap=0.05, jitter=0.0),
        )
        worker.lease_floor_s = 0.2
        t = threading.Thread(
            target=lambda: results.__setitem__(id(worker), worker.run(install_signals=False)), daemon=True
        )
        t.start()
        worker.exit_code = lambda: results.get(id(worker))  # type: ignore[attr-defined]
        workers.append((worker, t))
        return worker

    yield start
    for worker, t in workers:
        worker.request_stop(force=True)
        t.join(timeout=10)


def test_registration_payload(fake_server: FakeMangaVeyra, start_worker: Callable[..., Worker]) -> None:
    start_worker()
    deadline = time.monotonic() + 5
    while not fake_server.registrations and time.monotonic() < deadline:
        time.sleep(0.01)
    reg = fake_server.registrations[0]
    assert reg["name"] == "test-worker"
    assert reg["backends"] == ["mock"]
    assert reg["models"] == ["mock"]
    assert reg["max_concurrent"] == 1
    assert reg["gpus"] == [{"name": "Fake GPU", "vram_total_mb": 8192, "vram_free_mb": 8000}]
    assert "image.generate" in reg["job_types"] and "detect.panels" in reg["job_types"]
    assert "video.animate" not in reg["job_types"]  # never advertise what we cannot run
    assert reg["version"]


def test_generate_flow_uploads_every_variation(
    fake_server: FakeMangaVeyra, start_worker: Callable[..., Worker]
) -> None:
    rec = fake_server.enqueue(
        {
            "id": "g1",
            "type": "image.generate",
            "model": {"id": "mock", "manifest": {}},
            "input": {
                "prompt": "Rin Asagiri on the pier",
                "width": 256,
                "height": 384,
                "n": 3,
                "seed": 100,
                "steps": 4,
            },
        }
    )
    start_worker()
    rec = fake_server.wait("g1")
    assert rec.error is None, rec.error
    images = rec.result["output"]["images"]
    assert [img["seed"] for img in images] == [100, 101, 102]
    assert set(rec.artifacts) == {img["key"] for img in images}
    assert rec.result["actual"] == {"gpu_seconds": 0.0}
    assert fake_server.worker_id_headers == {"01JWORKER"}  # job calls name the lease holder
    assert rec.progress[0]["progress"] == 0.0  # "starting" is sent immediately
    fractions = [p["progress"] for p in rec.progress]
    assert fractions == sorted(fractions)  # monotonic


def test_cancel_via_progress_response(
    fake_server: FakeMangaVeyra, start_worker: Callable[..., Worker]
) -> None:
    fake_server.enqueue(
        {"id": "c1", "type": "image.generate", "input": {"width": 64, "height": 64, "n": 4, "steps": 200}}
    )
    fake_server.cancel_after_progress["c1"] = 3
    start_worker(mock_step_delay=0.005)
    rec = fake_server.wait("c1")
    assert rec.result is None
    assert rec.error == {"code": "canceled", "message": "job canceled (server)", "retryable": False}
    assert len(rec.artifacts) < 4


def test_cancel_via_heartbeat(fake_server: FakeMangaVeyra, start_worker: Callable[..., Worker]) -> None:
    fake_server.heartbeat_interval_s = 0.1
    fake_server.enqueue({"id": "c2", "type": "upscale", "input": {"seconds": 10}})
    fake_server.heartbeat_cancel.add("c2")
    started = time.monotonic()
    start_worker([SleepyBackend(Config(), [])])
    rec = fake_server.wait("c2")
    assert rec.error["code"] == "canceled"
    assert time.monotonic() - started < 5
    assert any("c2" in hb["running"] for hb in fake_server.heartbeats)


def test_failures_map_to_protocol_codes(
    fake_server: FakeMangaVeyra, start_worker: Callable[..., Worker]
) -> None:
    fake_server.enqueue({"id": "f1", "type": "upscale", "input": {"seconds": 0, "bad": True}})
    fake_server.enqueue({"id": "f2", "type": "upscale", "input": {"seconds": 0, "explode": True}})
    fake_server.enqueue({"id": "f3", "type": "image.generate", "input": {"width": 5}})
    worker = start_worker([SleepyBackend(Config(), []), MockBackend(Config(mock_step_delay=0), [])])
    assert fake_server.wait("f1").error == {"code": "bad_input", "message": "nope", "retryable": False}
    err = fake_server.wait("f2").error
    assert err["code"] == "internal" and err["retryable"] is True and "kaboom" in err["message"]
    assert fake_server.wait("f3").error["code"] == "bad_input"
    assert worker.failed == 3


def test_completes_with_gpu_seconds(fake_server: FakeMangaVeyra, start_worker: Callable[..., Worker]) -> None:
    fake_server.enqueue({"id": "s1", "type": "upscale", "input": {"seconds": 0.05}})
    start_worker([SleepyBackend(Config(), [])])
    assert fake_server.wait("s1").result == {"output": {"images": []}, "actual": {"gpu_seconds": 1.25}}


def test_lease_keepalive_for_quiet_jobs(
    fake_server: FakeMangaVeyra, start_worker: Callable[..., Worker]
) -> None:
    rec = fake_server.enqueue({"id": "l1", "type": "upscale", "lease_s": 1, "input": {"seconds": 1.2}})
    start_worker([SleepyBackend(Config(), [])])
    fake_server.wait("l1")
    # "starting" + at least two keep-alives while the backend was silent.
    assert len(rec.progress) >= 3


def test_backoff_on_claim_errors(fake_server: FakeMangaVeyra, start_worker: Callable[..., Worker]) -> None:
    fake_server.faults["/claim"] = deque([503, 500, 502, 503])
    fake_server.enqueue({"id": "b1", "type": "upscale", "input": {"seconds": 0}})
    start_worker([SleepyBackend(Config(), [])])
    assert fake_server.wait("b1").result is not None
    assert len(fake_server.claims) >= 1


def test_reregisters_when_server_forgets_worker(
    fake_server: FakeMangaVeyra, start_worker: Callable[..., Worker]
) -> None:
    fake_server.unknown_worker_once = True
    fake_server.enqueue({"id": "r1", "type": "upscale", "input": {"seconds": 0}})
    start_worker([SleepyBackend(Config(), [])])
    assert fake_server.wait("r1").result is not None
    assert len(fake_server.registrations) == 2


def test_concurrency_limit(fake_server: FakeMangaVeyra, start_worker: Callable[..., Worker]) -> None:
    SleepyBackend.peak = 0
    for i in range(4):
        fake_server.enqueue({"id": f"p{i}", "type": "upscale", "input": {"seconds": 0.4}})
    start_worker([SleepyBackend(Config(), [])], max_concurrent=2)
    for i in range(4):
        assert fake_server.wait(f"p{i}").result is not None
    assert SleepyBackend.peak == 2


def test_graceful_shutdown_hands_back_running_job(
    fake_server: FakeMangaVeyra, start_worker: Callable[..., Worker]
) -> None:
    rec = fake_server.enqueue({"id": "d1", "type": "upscale", "input": {"seconds": 30}})
    worker = start_worker([SleepyBackend(Config(), [])], drain_timeout_s=0.3)
    deadline = time.monotonic() + 5
    while not rec.progress and time.monotonic() < deadline:
        time.sleep(0.01)
    worker.request_stop()
    rec = fake_server.wait("d1", timeout=10)
    assert rec.error == {
        "code": "internal",
        "message": "worker shut down before the job finished",
        "retryable": True,
    }


def test_graceful_shutdown_waits_for_short_jobs(
    fake_server: FakeMangaVeyra, start_worker: Callable[..., Worker]
) -> None:
    rec = fake_server.enqueue({"id": "d2", "type": "upscale", "input": {"seconds": 0.5}})
    worker = start_worker([SleepyBackend(Config(), [])], drain_timeout_s=10)
    deadline = time.monotonic() + 5
    while not rec.progress and time.monotonic() < deadline:
        time.sleep(0.01)
    worker.request_stop()
    assert fake_server.wait("d2").result is not None


def test_stop_interrupts_long_poll_quickly(
    fake_server: FakeMangaVeyra, start_worker: Callable[..., Worker]
) -> None:
    fake_server.max_wait_s = 30  # the server would hold the claim for 30 s
    worker = start_worker(claim_wait_s=30)
    time.sleep(0.3)
    started = time.monotonic()
    worker.request_stop()
    deadline = time.monotonic() + 5
    while worker.exit_code() is None and time.monotonic() < deadline:  # type: ignore[attr-defined]
        time.sleep(0.02)
    assert worker.exit_code() == 0  # type: ignore[attr-defined]
    assert time.monotonic() - started < 3


def test_bad_token_exits_with_code_2(fake_server: FakeMangaVeyra, make_config: Callable[..., Config]) -> None:
    from mangaveyra_worker.client import ServerClient

    config = make_config(server=fake_server.url)
    worker = Worker(
        config,
        [MockBackend(config, [])],
        client=ServerClient(fake_server.url, "mvw_wrong"),
        gpu_probe=lambda: [],
    )
    assert worker.run(install_signals=False) == 2
