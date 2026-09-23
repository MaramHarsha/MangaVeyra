"""Regression tests for protocol mismatches found by cross-checking the worker against the Go server.

* The server sends ``"model": {"id": ""}`` for jobs without a model; the worker must treat that as "no model".
* A draining worker gets ``204`` without a long poll; the claim loop must not spin.
* ``409 lease_lost`` (and 403/404/410 on job calls) means "drop the job", never "token revoked" or "crash".
* Result reporting survives a server restart longer than a handful of retries, and a forced stop ends it.
* The VRAM cache never evicts a pipeline between its lookup and its use.
"""

from __future__ import annotations

import threading
import time
from pathlib import Path
from typing import Any, Callable, Iterator

import pytest

from mangaveyra_worker.backends.mock import MockBackend
from mangaveyra_worker.backoff import Backoff
from mangaveyra_worker.config import Config
from mangaveyra_worker.diffusion.cache import PipelineCache
from mangaveyra_worker.errors import Aborted, LeaseLost, Unauthorized
from mangaveyra_worker.jobs import Job
from mangaveyra_worker.runner import Worker
from tests.conftest import fast_client
from tests.fakes import FakeMangaVeyra

# ---------------------------------------------------------------------------------------------- job payloads


def test_empty_model_id_means_no_model(make_config: Callable[..., Config]) -> None:
    job = Job.from_payload({"id": "j", "type": "image.generate", "model": {"id": "", "manifest": {}}})
    assert job.model_id is None
    assert MockBackend(make_config(), []).accepts(job)
    named = Job.from_payload({"id": "j", "type": "image.generate", "model": {"id": "mock"}})
    assert named.model_id == "mock"


@pytest.mark.parametrize(
    "payload",
    [
        {"id": "j", "type": "ocr", "model": "flux"},
        {"id": "j", "type": "ocr", "input": ["prompt"]},
        {"id": "j", "type": "ocr", "files": "https://x"},
        ["not", "an", "object"],
    ],
)
def test_malformed_job_payloads_are_value_errors(payload: Any) -> None:
    with pytest.raises(ValueError):
        Job.from_payload(payload)


# ---------------------------------------------------------------------------------------------- runner


@pytest.fixture
def worker_factory(
    fake_server: FakeMangaVeyra, make_config: Callable[..., Config]
) -> Iterator[Callable[..., Worker]]:
    started: list[Worker] = []

    def start(**cfg: Any) -> Worker:
        config = make_config(server=fake_server.url, **cfg)
        worker = Worker(
            config,
            [MockBackend(config, [])],
            client=fast_client(fake_server.url),
            gpu_probe=lambda: [],
            claim_backoff=Backoff(base=0.01, cap=0.05, jitter=0.0),
        )
        worker.lease_floor_s = 0.2
        threading.Thread(target=lambda: worker.run(install_signals=False), daemon=True).start()
        started.append(worker)
        return worker

    yield start
    for w in started:
        w.request_stop(force=True)


def test_model_less_job_runs_end_to_end(
    fake_server: FakeMangaVeyra, worker_factory: Callable[..., Worker]
) -> None:
    rec = fake_server.enqueue(
        {"id": "nomodel", "type": "image.generate", "model": {"id": "", "manifest": {}}, "input": {"n": 1}}
    )
    worker_factory()
    fake_server.wait("nomodel")
    assert rec.error is None, rec.error
    assert len(rec.result["output"]["images"]) == 1


def test_immediate_204_does_not_spin(fake_server: FakeMangaVeyra, worker_factory: Callable[..., Worker]) -> None:
    fake_server.no_long_poll = True
    worker_factory(claim_wait_s=20)
    time.sleep(0.5)
    before = len(fake_server.claims)
    time.sleep(2.0)
    # Backoff 0.5 s → 1 s → 2 s …: a handful of claims, not hundreds.
    assert len(fake_server.claims) - before <= 5
    # A job still gets picked up.
    fake_server.enqueue({"id": "late", "type": "upscale", "input": {"scale": 2}, "files": {}})
    rec = fake_server.wait("late", timeout=15)
    assert rec.done.is_set()


def test_lease_lost_on_complete_drops_job_quietly(
    fake_server: FakeMangaVeyra, worker_factory: Callable[..., Worker]
) -> None:
    fake_server.enqueue({"id": "gone", "type": "image.generate", "input": {"n": 1}})
    fake_server.lease_lost.add("gone")
    worker = worker_factory()
    deadline = time.monotonic() + 10
    while ("gone", "complete") not in fake_server.job_calls and ("gone", "fail") not in fake_server.job_calls:
        assert time.monotonic() < deadline, fake_server.job_calls
        time.sleep(0.02)
    time.sleep(0.3)
    actions = [a for j, a in fake_server.job_calls if j == "gone"]
    # progress said cancel → the job stops; its fail report hits 409 once and is not repeated or escalated.
    assert actions.count("fail") + actions.count("complete") == 1, actions
    assert worker.completed == 0
    # The worker keeps serving other jobs.
    fake_server.enqueue({"id": "next", "type": "image.generate", "input": {"n": 1}})
    assert fake_server.wait("next").result is not None


def test_lease_lost_on_upload_is_not_reported(
    fake_server: FakeMangaVeyra, worker_factory: Callable[..., Worker]
) -> None:
    fake_server.enqueue({"id": "moved", "type": "image.generate", "input": {"n": 2}})
    worker = worker_factory()
    # Lose the lease as soon as the job starts.
    deadline = time.monotonic() + 10
    while not any(j == "moved" for j, _ in fake_server.job_calls):
        assert time.monotonic() < deadline
        time.sleep(0.005)
    fake_server.lease_lost.add("moved")
    time.sleep(1.0)
    actions = [a for j, a in fake_server.job_calls if j == "moved"]
    assert "complete" not in actions or actions.count("complete") == 1
    assert worker.completed == 0


# ---------------------------------------------------------------------------------------------- client


def test_job_call_errors_map_to_lease_lost(fake_server: FakeMangaVeyra) -> None:
    fake_server.enqueue({"id": "j", "type": "image.generate"})
    c = fast_client(fake_server.url)
    for status in (403, 404, 409, 410):
        fake_server.faults["/jobs/*/complete"].append(status)
        with pytest.raises(LeaseLost):
            c.complete("j", {}, {})
    # Outside job calls a 403 is still a rejected token.
    fake_server.faults["/claim"].append(403)
    with pytest.raises(Unauthorized):
        c.claim("w", ["ocr"], [], 0)
    # progress on a lost lease means "stop".
    fake_server.faults["/jobs/*/progress"].append(409)
    assert c.progress("j", 0.5) is True


def test_fail_normalises_codes_and_reports_actual(fake_server: FakeMangaVeyra) -> None:
    rec = fake_server.enqueue({"id": "j", "type": "image.generate"})
    c = fast_client(fake_server.url)
    c.fail("j", "exploded", "boom", True, actual={"gpu_seconds": 2.5})
    assert rec.error == {"code": "internal", "message": "boom", "retryable": True}


def test_result_reporting_outlasts_a_server_restart(fake_server: FakeMangaVeyra) -> None:
    rec = fake_server.enqueue({"id": "j", "type": "image.generate"})
    c = fast_client(fake_server.url)
    fake_server.faults["/jobs/*/complete"].extend([503] * 15)  # more than any fixed attempt count
    c.complete("j", {"images": []}, {"gpu_seconds": 1})
    assert rec.result == {"output": {"images": []}, "actual": {"gpu_seconds": 1}}


def test_result_reporting_gives_up_after_the_window(fake_server: FakeMangaVeyra) -> None:
    fake_server.enqueue({"id": "j", "type": "image.generate"})
    c = fast_client(fake_server.url, report_window_s=0.3)
    fake_server.faults["/jobs/*/complete"].extend([503] * 1000)
    started = time.monotonic()
    with pytest.raises(Exception) as err:
        c.complete("j", {}, {})
    assert getattr(err.value, "status", None) == 503
    assert time.monotonic() - started < 2


def test_forced_stop_ends_result_retries(fake_server: FakeMangaVeyra) -> None:
    fake_server.enqueue({"id": "j", "type": "image.generate"})
    c = ServerClientWithSlowBackoff(fake_server.url)
    fake_server.faults["/jobs/*/fail"].extend([503] * 1000)
    threading.Timer(0.3, c.hard_abort.set).start()
    started = time.monotonic()
    with pytest.raises(Aborted):
        c.fail("j", "internal", "x", True)
    assert time.monotonic() - started < 2


def ServerClientWithSlowBackoff(url: str) -> Any:  # noqa: N802 - reads like a class in the tests
    """A client whose retry sleeps are long enough that only an abort can end them quickly."""
    from mangaveyra_worker.client import ServerClient
    from tests.fakes import TOKEN

    return ServerClient(url, TOKEN, backoff_factory=lambda: Backoff(base=5.0, cap=5.0, jitter=0.0))


# ---------------------------------------------------------------------------------------------- cache


def test_checked_out_pipeline_is_never_evicted() -> None:
    evicted: list[str] = []
    cache = PipelineCache(10000, on_evict=lambda pipe: evicted.append(pipe))
    gate, inside = threading.Event(), threading.Event()

    def job() -> None:
        with cache.checkout("a", 6000, lambda: "A") as entry:
            inside.set()
            gate.wait(5)
            assert entry.pipe == "A"

    t = threading.Thread(target=job)
    t.start()
    assert inside.wait(5)
    # A second model that does not fit alongside "a" loads anyway, but "a" stays resident.
    with cache.checkout("b", 6000, lambda: "B"):
        pass
    assert "A" not in evicted
    cache.clear()  # training/shutdown: idle pipelines go, the checked-out one stays
    assert cache.keys() == ["a"] and evicted == ["B"]
    gate.set()
    t.join()
    assert cache.get("a", 6000, lambda: "never").pins == 0
    cache.clear()
    assert cache.keys() == []


def test_pin_is_taken_with_the_lookup() -> None:
    """Between get(pin=True) and taking the lock, a concurrent load must not evict the entry."""
    cache = PipelineCache(10000)
    entry = cache.get("a", 8000, lambda: "A", pin=True)
    cache.get("b", 8000, lambda: "B")  # needs room: "a" is pinned but idle (lock free)
    assert "a" in cache.keys()
    with cache._lock:  # release the manual pin like checkout() does
        entry.pins -= 1
    cache.get("c", 8000, lambda: "C")
    assert "a" not in cache.keys()


def test_worker_scratch_dir_is_removed(tmp_path: Path) -> None:
    """Scratch space of finished jobs does not pile up on disk (memory/disk hygiene)."""
    from mangaveyra_worker.jobs import JobContext
    from tests.conftest import RecordingClient

    job = Job.from_payload({"id": "01JSCRATCH", "type": "ocr"})
    ctx = JobContext(job, RecordingClient(), scratch_root=tmp_path)  # type: ignore[arg-type]
    (ctx.scratch / "big.bin").write_bytes(b"x" * 1024)
    ctx.cleanup()
    assert list(tmp_path.iterdir()) == []


@pytest.mark.parametrize(
    ("trigger", "ok"),
    [
        ("rin_asagiri", True),
        ("Rin Asagiri", True),
        ("凛", True),
        ("--output_dir=/etc", False),
        ("-rin", False),
        ("rin;rm -rf /", False),
        ("rin\nnext", False),
        ("x" * 65, False),
        ("", False),
    ],
)
def test_trigger_words_are_validated(trigger: str, ok: bool) -> None:
    from mangaveyra_worker.errors import BadInput
    from mangaveyra_worker.jobs import validate_trigger

    if ok:
        assert validate_trigger(trigger) == trigger
    else:
        with pytest.raises(BadInput):
            validate_trigger(trigger)
