"""The worker main loop: register, heartbeat, claim, execute, report.

Threads:

* **main** — the claim loop: long-polls ``claim`` whenever a slot is free and hands jobs to the executor pool.
* **heartbeat** — every ``heartbeat_interval_s``: GPU stats + running job ids; applies the server's cancel list.
* **lease** — resends progress for jobs that have been quiet for a third of their lease (model loading, long
  uploads) so the server does not hand them to another worker.
* **pool** — ``max_concurrent`` job threads.

Shutdown (SIGINT/SIGTERM): stop claiming, let running jobs finish for ``drain_timeout_s``, then cancel them and
report them back as retryable so the server re-queues them. A claim still in flight is abandoned (a job it
returns is handed straight back). A second signal cancels immediately and stops every retry.

Lease loss: when the server answers a job call with ``lease_lost`` (the job was re-queued after missed heartbeats,
canceled, or finished by a retried request) the job is dropped silently — reporting it again would only fail.
"""

from __future__ import annotations

import logging
import signal
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from typing import Any, Callable

from mangaveyra_worker import __version__
from mangaveyra_worker.backends.base import Backend
from mangaveyra_worker.backoff import Backoff
from mangaveyra_worker.client import ServerClient
from mangaveyra_worker.config import Config
from mangaveyra_worker.errors import (
    Aborted,
    ApiError,
    JobCanceled,
    JobFailure,
    LeaseLost,
    Unauthorized,
    UnknownWorker,
)
from mangaveyra_worker.gpu import detect_gpus, protocol_gpus
from mangaveyra_worker.jobs import Job, JobContext
from mangaveyra_worker.log import kv

log = logging.getLogger(__name__)


class Worker:
    """One worker process talking to one server."""

    def __init__(
        self,
        config: Config,
        backends: list[Backend],
        *,
        client: ServerClient | None = None,
        gpu_probe: Callable[[], list[dict[str, Any]]] = detect_gpus,
        claim_backoff: Backoff | None = None,
    ) -> None:
        self.config = config
        self.backends = backends
        self.stop_event = threading.Event()
        self.client = client or ServerClient(
            config.server,
            config.token,
            verify=config.tls_verify,
            abort=self.stop_event,
            max_download_bytes=config.max_download_mb * 1024 * 1024,
        )
        self.client.abort = self.stop_event
        self._gpu_probe = gpu_probe
        self._claim_backoff = claim_backoff or Backoff(base=1.0, cap=60.0)
        #: Paces claims when the server answers 204 without long-polling (a draining worker, a proxy).
        self._idle_backoff = Backoff(base=0.5, cap=5.0)
        #: A 204 faster than this is treated as "did not long-poll".
        self.min_poll_s = 1.0
        self.worker_id: str | None = None
        self.heartbeat_interval = 15.0
        self.lease_s = 120
        self._running: dict[str, JobContext] = {}
        self._running_lock = threading.Lock()
        self._slots = threading.BoundedSemaphore(config.max_concurrent)
        self._pool = ThreadPoolExecutor(max_workers=config.max_concurrent, thread_name_prefix="job")
        self._threads: list[threading.Thread] = []
        self._force = threading.Event()
        self._claim_thread: threading.Thread | None = None
        #: A job silent for max(lease_floor_s, lease_s / 3) gets a keep-alive progress call.
        self.lease_floor_s = 5.0
        self.completed = 0
        self.failed = 0

    # ------------------------------------------------------------------ capabilities

    def job_types(self) -> list[str]:
        """Union of every backend's job types (the only types this worker claims)."""
        seen: dict[str, None] = {}
        for b in self.backends:
            for t in b.job_types():
                seen[t] = None
        return list(seen)

    def models(self) -> list[str]:
        """Union of every backend's model ids."""
        seen: dict[str, None] = {}
        for b in self.backends:
            for m in b.models():
                seen[m] = None
        return list(seen)

    def backend_for(self, job: Job) -> Backend | None:
        """The first backend (in configured order) that accepts ``job``."""
        return next((b for b in self.backends if b.accepts(job)), None)

    def registration(self) -> dict[str, Any]:
        """The ``register`` payload."""
        return {
            "name": self.config.name,
            "version": __version__,
            "backends": [b.name for b in self.backends],
            "gpus": protocol_gpus(self._gpu_probe()),
            "job_types": self.job_types(),
            "models": self.models(),
            "max_concurrent": self.config.max_concurrent,
        }

    # ------------------------------------------------------------------ lifecycle

    def register(self) -> None:
        """Register (or re-register) with the server; retries until it succeeds or the worker stops."""
        payload = self.registration()
        data = self.client.register(payload)
        if not data.get("worker_id"):
            raise ApiError(502, "bad_response", "register returned no worker_id")
        self.worker_id = str(data["worker_id"])
        self.client.worker_id = self.worker_id
        self.heartbeat_interval = float(data.get("heartbeat_interval_s") or 15)
        self.lease_s = int(data.get("lease_s") or 120)
        self._touch_health()
        log.info(
            "registered",
            extra=kv(
                worker_id=self.worker_id,
                name=payload["name"],
                backends=payload["backends"],
                job_types=len(payload["job_types"]),
                models=payload["models"],
                gpus=len(payload["gpus"]),
            ),
        )

    def run(self, install_signals: bool = True) -> int:
        """Run until stopped. Returns a process exit code (0 clean, 2 token rejected)."""
        if install_signals and threading.current_thread() is threading.main_thread():
            self._install_signals()
        try:
            self.register()
        except Unauthorized as exc:
            log.error("worker token rejected by the server", extra=kv(error=str(exc)))
            return 2
        except Aborted:
            return 0
        except ApiError as exc:
            log.error("registration failed", extra=kv(error=str(exc)))
            return 1
        self._start_thread("heartbeat", self._heartbeat_loop)
        self._start_thread("lease", self._lease_loop)
        code = 0
        try:
            self._claim_loop()
        except Unauthorized as exc:
            log.error("worker token revoked", extra=kv(error=str(exc)))
            code = 2
        finally:
            self._shutdown()
        return code

    def request_stop(self, force: bool = False) -> None:
        """Begin a graceful shutdown (``force`` cancels running jobs immediately)."""
        if force or self.stop_event.is_set():
            self._force.set()
            self.client.hard_abort.set()
        self.stop_event.set()

    def _install_signals(self) -> None:
        def handler(signum: int, _frame: Any) -> None:
            first = not self.stop_event.is_set()
            log.warning(
                "shutdown requested" if first else "second signal: canceling running jobs",
                extra=kv(signal=signal.Signals(signum).name, running=len(self._running)),
            )
            self.request_stop(force=not first)

        signal.signal(signal.SIGINT, handler)
        signal.signal(signal.SIGTERM, handler)

    def _start_thread(self, name: str, target: Callable[[], None]) -> None:
        t = threading.Thread(target=target, name=name, daemon=True)
        t.start()
        self._threads.append(t)

    # ------------------------------------------------------------------ claim loop

    def _claim_loop(self) -> None:
        job_types, models = self.job_types(), self.models()
        while not self.stop_event.is_set():
            if not self._slots.acquire(timeout=0.5):
                continue
            handed_off = False
            try:
                if self.stop_event.is_set():
                    break
                polled_at = time.monotonic()
                payload = self._claim(job_types, models)
                self._claim_backoff.reset()
                self._touch_health()
                if payload is None:
                    if time.monotonic() - polled_at < self.min_poll_s:
                        # 204 without a long poll: pace the loop instead of spinning on the server.
                        if self._idle_backoff.sleep(self.stop_event):
                            break
                    else:
                        self._idle_backoff.reset()
                    continue
                self._idle_backoff.reset()
                self._accept(payload)
                handed_off = True
            except Aborted:
                break
            except Unauthorized:
                raise
            except UnknownWorker:
                log.warning("server forgot this worker, re-registering")
                try:
                    self.register()
                except Aborted:
                    break
            except ApiError as exc:
                delay_hit = self._claim_backoff.sleep(self.stop_event)
                log.warning("claim failed", extra=kv(error=str(exc), attempt=self._claim_backoff.attempt))
                if delay_hit:
                    break
            finally:
                if not handed_off:
                    self._slots.release()

    def _claim(self, job_types: list[str], models: list[str]) -> dict[str, Any] | None:
        """Long-poll ``claim`` on a helper thread so a shutdown signal is honoured within ~0.25 s.

        If the worker stops while a claim is in flight, the claim is abandoned; should it still return a job, the
        job is handed straight back as retryable (and, failing that, the server re-queues it once the lease
        expires or the worker misses its heartbeats).
        """
        box: dict[str, Any] = {"abandoned": False}
        done = threading.Event()
        lock = threading.Lock()

        def target() -> None:
            try:
                job = self.client.claim(self.worker_id or "", job_types, models, self.config.claim_wait_s)
            except BaseException as exc:  # noqa: BLE001 - re-raised on the claim loop thread
                box["error"] = exc
                done.set()
                return
            with lock:
                if box["abandoned"]:
                    if job:
                        self._safe_fail(str(job.get("id")), "internal", "worker shutting down", True)
                    return
                box["job"] = job
                done.set()

        t = threading.Thread(target=target, name="claim", daemon=True)
        t.start()
        self._claim_thread = t
        while not done.wait(0.25):
            if self.stop_event.is_set():
                with lock:
                    if not done.is_set():
                        box["abandoned"] = True
                        raise Aborted("claim abandoned by shutdown")
                break
        if "error" in box:
            raise box["error"]
        return box.get("job")

    def _accept(self, payload: dict[str, Any]) -> None:
        """Start a claimed job on the pool (the slot is released when it finishes)."""
        try:
            job = Job.from_payload(payload)
        except (ValueError, TypeError) as exc:
            log.error("server sent an invalid job", extra=kv(error=str(exc)))
            if payload.get("id"):
                self._safe_fail(str(payload["id"]), "bad_input", f"invalid job payload: {exc}", False)
            raise ApiError(0, "bad_job", str(exc)) from exc
        ctx = JobContext(job, self.client, scratch_root=self.config.data_dir / "jobs")
        with self._running_lock:
            self._running[job.id] = ctx
        if self.stop_event.is_set():
            ctx.cancel("shutdown")
        log.info("claimed", extra=kv(job=job.id, type=job.type, model=job.model_id, attempt=job.attempt))
        self._pool.submit(self._execute, job, ctx)

    # ------------------------------------------------------------------ execution

    def _execute(self, job: Job, ctx: JobContext) -> None:
        started = time.monotonic()
        try:
            backend = self.backend_for(job)
            if backend is None:
                raise JobFailure(
                    f"no backend on this worker accepts {job.type} with model {job.model_id}",
                    code="bad_input",
                    retryable=True,
                )
            ctx.progress(0.0, message=f"starting on {backend.name}", force=True)
            result = backend.run(job, ctx)
            ctx.check()
            actual = {"gpu_seconds": round(result.gpu_seconds, 2)}
            try:
                self.client.complete(job.id, result.output, actual)
            except LeaseLost as exc:
                # A retried complete whose first attempt landed also ends here; either way the job is settled.
                log.warning(
                    "lease lost before complete; result dropped", extra=kv(job=job.id, error=str(exc))
                )
                return
            self.completed += 1
            log.info(
                "completed",
                extra=kv(
                    job=job.id,
                    type=job.type,
                    backend=backend.name,
                    seconds=round(time.monotonic() - started, 2),
                    artifacts=len(ctx.uploaded),
                ),
            )
        except LeaseLost as exc:
            log.warning("lease lost; job dropped", extra=kv(job=job.id, type=job.type, error=str(exc)))
        except Aborted as exc:
            log.error("result not reported (forced shutdown)", extra=kv(job=job.id, error=str(exc)))
        except JobCanceled as exc:
            if ctx.cancel_reason == "shutdown":
                self._report_failure(job, "internal", "worker shut down before the job finished", True)
            else:
                self._report_failure(job, "canceled", exc.message, False)
        except JobFailure as exc:
            self._report_failure(job, exc.code, exc.message, exc.retryable)
        except MemoryError:
            self._report_failure(job, "oom", "host ran out of memory", True)
        except Exception as exc:
            if type(exc).__name__ in ("OutOfMemoryError", "OutOfMemory"):  # torch.cuda.OutOfMemoryError
                _free_accelerator_memory()
                self._report_failure(job, "oom", f"accelerator out of memory: {exc}", True)
            else:
                log.exception("job crashed", extra=kv(job=job.id, type=job.type))
                self._report_failure(job, "internal", f"{type(exc).__name__}: {exc}", True)
        finally:
            ctx.cleanup()
            with self._running_lock:
                self._running.pop(job.id, None)
            self._slots.release()

    def _report_failure(self, job: Job, code: str, message: str, retryable: bool) -> None:
        self.failed += 1
        log.warning(
            "failed", extra=kv(job=job.id, type=job.type, code=code, error=message, retryable=retryable)
        )
        self._safe_fail(job.id, code, message, retryable)

    def _safe_fail(self, job_id: str, code: str, message: str, retryable: bool) -> None:
        try:
            self.client.fail(job_id, code, message, retryable)
        except LeaseLost as exc:
            log.info("failure not reported: lease already gone", extra=kv(job=job_id, error=str(exc)))
        except (ApiError, Aborted) as exc:
            log.error("could not report failure", extra=kv(job=job_id, error=str(exc)))

    # ------------------------------------------------------------------ background loops

    def running_ids(self) -> list[str]:
        """Ids of jobs currently executing."""
        with self._running_lock:
            return list(self._running)

    def _cancel(self, job_ids: list[str], reason: str) -> None:
        with self._running_lock:
            ctxs = [self._running[j] for j in job_ids if j in self._running]
        for ctx in ctxs:
            log.info("cancel requested", extra=kv(job=ctx.job.id, reason=reason))
            ctx.cancel(reason)

    def heartbeat_once(self) -> None:
        """Send one heartbeat and apply its cancel list."""
        gpus = protocol_gpus(self._gpu_probe())
        cancel = self.client.heartbeat(self.worker_id or "", gpus, self.running_ids())
        self._touch_health()
        if cancel:
            self._cancel(cancel, "heartbeat")

    def _touch_health(self) -> None:
        """Update the health file's mtime (the container HEALTHCHECK compares it with the clock)."""
        path = self.config.health_file
        if path is None:
            return
        try:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.touch()
        except OSError as exc:
            log.debug("cannot touch health file: %s", exc)

    def _heartbeat_loop(self) -> None:
        while not self.stop_event.wait(self.heartbeat_interval):
            try:
                self.heartbeat_once()
            except Aborted:
                return
            except UnknownWorker:
                log.warning("heartbeat: server forgot this worker; the claim loop re-registers")
            except ApiError as exc:
                log.warning("heartbeat failed", extra=kv(error=str(exc)))
        # Keep heartbeating while draining so the server does not declare the worker offline mid-job.
        while self.running_ids() and not self._force.wait(self.heartbeat_interval):
            try:
                self.heartbeat_once()
            except (ApiError, Aborted):
                pass

    def _lease_loop(self) -> None:
        while True:
            with self._running_lock:
                ctxs = list(self._running.values())
            if self.stop_event.is_set() and not ctxs:
                return
            now = time.monotonic()
            for ctx in ctxs:
                quiet_limit = max(self.lease_floor_s, ctx.job.lease_s / 3)
                if now - ctx.last_contact >= quiet_limit and not ctx.canceled:
                    ctx.keepalive()
            time.sleep(min(1.0, self.lease_floor_s / 2))

    # ------------------------------------------------------------------ shutdown

    def _shutdown(self) -> None:
        self.stop_event.set()
        deadline = time.monotonic() + self.config.drain_timeout_s
        if self.running_ids():
            log.info(
                "draining running jobs",
                extra=kv(running=len(self.running_ids()), timeout_s=self.config.drain_timeout_s),
            )
        while self.running_ids() and time.monotonic() < deadline and not self._force.is_set():
            time.sleep(0.2)
        leftover = self.running_ids()
        if leftover:
            self._cancel(leftover, "shutdown")
        self._pool.shutdown(wait=True)
        self._force.set()
        if self._claim_thread is not None:
            # Give an abandoned claim a moment to hand a just-claimed job back; closing the client below then
            # drops the long-poll connection, which makes the server end the poll without claiming anything.
            self._claim_thread.join(timeout=2.0)
        for backend in self.backends:
            try:
                backend.close()
            except Exception as exc:  # pragma: no cover - best effort
                log.warning("backend close failed", extra=kv(backend=backend.name, error=str(exc)))
        self.client.close()
        log.info("stopped", extra=kv(completed=self.completed, failed=self.failed))


def _free_accelerator_memory() -> None:
    import sys

    torch = sys.modules.get("torch")
    if torch is not None:
        try:
            torch.cuda.empty_cache()
        except Exception:  # pragma: no cover
            pass
