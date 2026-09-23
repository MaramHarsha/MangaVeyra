"""HTTP client for the worker protocol v1 (``/api/worker/v1``).

The client is thread-safe (one ``httpx.Client`` connection pool shared by the claim loop, the heartbeat thread
and every running job). Transient failures — network errors, 5xx, 408/425/429 — are retried with exponential
backoff; how many times depends on the call (see ``attempts`` on each method). Result-reporting calls (artifact
upload, complete, fail) keep retrying for ``report_window_s`` so a finished job survives a server restart.

Job calls map "this job is not yours any more" answers — ``409 lease_lost``, and ``403``/``404``/``410`` on
``/jobs/{id}/…`` — to :class:`~mangaveyra_worker.errors.LeaseLost`, which the runner handles by dropping the job.
"""

from __future__ import annotations

import json
import logging
import threading
import time
from typing import Any, Callable, Mapping
from urllib.parse import urljoin

import httpx

from mangaveyra_worker import __version__
from mangaveyra_worker.backoff import Backoff
from mangaveyra_worker.errors import Aborted, ApiError, BadInput, LeaseLost, Unauthorized, UnknownWorker

log = logging.getLogger(__name__)

API_PREFIX = "/api/worker/v1"

#: Codes the server may use when it no longer knows a worker id.
_UNKNOWN_WORKER_CODES = {"unknown_worker", "worker_not_found", "not_registered"}


class ServerClient:
    """Talks to one MangaVeyra server with a worker token.

    ``abort`` is an event the runner sets on shutdown: retry sleeps of *interruptible* calls (claim,
    heartbeat, register) stop early with :class:`Aborted`, while result-reporting calls (upload, complete,
    fail) keep retrying so a finished job is not lost — until ``hard_abort`` is set (a second signal) or
    ``report_window_s`` has passed.
    """

    def __init__(
        self,
        server: str,
        token: str,
        *,
        timeout: float = 30.0,
        verify: bool = True,
        transport: httpx.BaseTransport | None = None,
        abort: threading.Event | None = None,
        backoff_factory: Callable[[], Backoff] | None = None,
        max_download_bytes: int = 64 * 1024 * 1024,
        report_window_s: float = 300.0,
    ) -> None:
        self.server = server.rstrip("/")
        self.abort = abort or threading.Event()
        #: Set on a forced shutdown: even result-reporting calls stop retrying.
        self.hard_abort = threading.Event()
        #: How long upload/complete/fail keep retrying transient failures (server restart, network blip).
        self.report_window_s = report_window_s
        #: Set after registration; sent as ``X-Worker-Id`` on job calls so the server can check the lease holder.
        self.worker_id: str | None = None
        self._backoff_factory = backoff_factory or (lambda: Backoff(base=0.5, cap=30.0))
        self._max_download = max_download_bytes
        headers = {
            "Authorization": f"Bearer {token}",
            "User-Agent": f"mangaveyra-worker/{__version__}",
            "Accept": "application/json",
        }
        self._http = httpx.Client(
            base_url=self.server + API_PREFIX,
            headers=headers,
            timeout=timeout,
            verify=verify,
            transport=transport,
        )
        # Signed input URLs must be fetched without the worker token (they may point at an S3 bucket).
        self._files = httpx.Client(
            timeout=timeout,
            verify=verify,
            transport=transport,
            follow_redirects=True,
            headers={"User-Agent": headers["User-Agent"]},
        )

    # ------------------------------------------------------------------ protocol calls

    def register(self, payload: Mapping[str, Any]) -> dict[str, Any]:
        """``POST /register`` → ``{"worker_id", "heartbeat_interval_s", "lease_s"}``. Retries until success,
        a non-retryable error, or shutdown."""
        return self._call("POST", "/register", json=dict(payload), attempts=None, interruptible=True) or {}

    def heartbeat(self, worker_id: str, gpus: list[dict[str, Any]], running: list[str]) -> list[str]:
        """``POST /heartbeat``; returns the ids of running jobs the server wants canceled."""
        body = {"worker_id": worker_id, "gpus": gpus, "running": running}
        data = self._call("POST", "/heartbeat", json=body, attempts=2, interruptible=True) or {}
        return [str(x) for x in data.get("cancel") or []]

    def claim(
        self, worker_id: str, job_types: list[str], models: list[str], wait_s: int
    ) -> dict[str, Any] | None:
        """``POST /claim`` long poll. Returns the job object or ``None`` on ``204``. Single attempt: the claim
        loop owns the backoff so it can re-register and shut down cleanly."""
        body = {"worker_id": worker_id, "job_types": job_types, "models": models, "wait_s": wait_s}
        data = self._call("POST", "/claim", json=body, attempts=1, interruptible=True, timeout=wait_s + 15.0)
        if not data:
            return None
        job = data.get("job")
        return job if isinstance(job, dict) else None

    def progress(
        self,
        job_id: str,
        progress: float,
        *,
        step: int | None = None,
        steps: int | None = None,
        message: str | None = None,
        preview_png_b64: str | None = None,
    ) -> bool:
        """``POST /jobs/{id}/progress`` (also extends the lease). Returns ``True`` when the job must stop."""
        body: dict[str, Any] = {"progress": round(min(1.0, max(0.0, float(progress))), 4)}
        if step is not None:
            body["step"] = int(step)
        if steps is not None:
            body["steps"] = int(steps)
        if message:
            body["message"] = message[:200]
        if preview_png_b64:
            body["preview_png_b64"] = preview_png_b64
        try:
            data = self._call("POST", f"/jobs/{job_id}/progress", json=body, attempts=3) or {}
        except LeaseLost:
            return True  # the lease is gone or the job was finished elsewhere: stop working on it
        return bool(data.get("cancel"))

    def upload_artifact(
        self,
        job_id: str,
        name: str,
        data: bytes,
        *,
        filename: str | None = None,
        content_type: str = "image/png",
        meta: Mapping[str, Any] | None = None,
    ) -> str:
        """``POST /jobs/{id}/artifacts`` (multipart). Returns the storage key the server assigned."""
        form = {"name": name}
        if meta:
            form["meta"] = json.dumps(meta, separators=(",", ":"), default=str)
        files = {"file": (filename or name, data, content_type)}
        # The server requires ``name`` before ``file`` in the multipart body; httpx writes data fields first.
        result = self._call(
            "POST",
            f"/jobs/{job_id}/artifacts",
            data=form,
            files=files,
            attempts=None,
            deadline_s=self.report_window_s,
            timeout=300.0,
        )
        key = (result or {}).get("key")
        if not key:
            raise ApiError(502, "bad_response", "artifact upload returned no key")
        return str(key)

    def complete(self, job_id: str, output: Mapping[str, Any], actual: Mapping[str, Any]) -> None:
        """``POST /jobs/{id}/complete``."""
        body = {"output": dict(output), "actual": dict(actual)}
        self._call(
            "POST", f"/jobs/{job_id}/complete", json=body, attempts=None, deadline_s=self.report_window_s
        )

    def fail(
        self,
        job_id: str,
        code: str,
        message: str,
        retryable: bool,
        actual: Mapping[str, Any] | None = None,
    ) -> None:
        """``POST /jobs/{id}/fail`` (``actual`` optionally reports the GPU time the failed attempt used)."""
        if code not in ERROR_CODES:
            code = "internal"
        error = {"code": code, "message": message[:1000], "retryable": bool(retryable)}
        body: dict[str, Any] = {"error": error}
        if actual:
            body["actual"] = dict(actual)
        self._call("POST", f"/jobs/{job_id}/fail", json=body, attempts=None, deadline_s=self.report_window_s)

    def download(self, url: str) -> bytes:
        """GET a signed input URL (no auth header). Relative URLs resolve against the server."""
        if not url.startswith(("http://", "https://")):
            url = urljoin(self.server + "/", url.lstrip("/"))
        backoff = self._backoff_factory()
        for attempt in range(4):
            try:
                with self._files.stream("GET", url) as resp:
                    if resp.status_code >= 400:
                        if resp.status_code < 500 and resp.status_code not in (408, 429):
                            raise BadInput(f"input file download failed: HTTP {resp.status_code}")
                        raise httpx.HTTPStatusError("server error", request=resp.request, response=resp)
                    declared = int(resp.headers.get("content-length") or 0)
                    if declared > self._max_download:
                        raise BadInput(f"input file too large ({declared} bytes)")
                    buf = bytearray()
                    for chunk in resp.iter_bytes():
                        buf.extend(chunk)
                        if len(buf) > self._max_download:
                            raise BadInput("input file too large")
                    return bytes(buf)
            except (httpx.TransportError, httpx.HTTPStatusError) as exc:
                if attempt == 3:
                    raise ApiError(502, "download_failed", f"cannot download input file: {exc}") from exc
                time.sleep(backoff.next())
        raise AssertionError("unreachable")  # pragma: no cover

    def close(self) -> None:
        """Close both connection pools."""
        self._http.close()
        self._files.close()

    # ------------------------------------------------------------------ transport

    def _call(
        self,
        method: str,
        path: str,
        *,
        attempts: int | None,
        interruptible: bool = False,
        timeout: float | None = None,
        deadline_s: float | None = None,
        **kwargs: Any,
    ) -> dict[str, Any] | None:
        """Send one request with retries.

        ``attempts=None`` retries until ``deadline_s`` seconds have passed (forever when that is ``None``),
        until shutdown for *interruptible* calls, and until :attr:`hard_abort` for every call.
        Returns the decoded JSON body, or ``None`` for ``204 No Content``.
        """
        backoff = self._backoff_factory()
        tried = 0
        give_up_at = None if deadline_s is None else time.monotonic() + deadline_s
        while True:
            tried += 1
            try:
                req_kwargs = dict(kwargs)
                if timeout is not None:
                    req_kwargs["timeout"] = timeout
                if self.worker_id and path.startswith("/jobs/"):
                    req_kwargs["headers"] = {"X-Worker-Id": self.worker_id}
                resp = self._http.request(method, path, **req_kwargs)
                if resp.status_code == 204:
                    return None
                if resp.status_code < 300:
                    if not resp.content:
                        return {}
                    try:
                        return resp.json()
                    except ValueError as exc:
                        raise ApiError(resp.status_code, "bad_response", "response is not JSON") from exc
                raise _api_error(resp, path)
            except (httpx.TransportError, ApiError) as exc:
                retryable = isinstance(exc, httpx.TransportError) or exc.retryable
                out_of_time = give_up_at is not None and time.monotonic() >= give_up_at
                exhausted = (attempts is not None and tried >= attempts) or out_of_time
                if not retryable or exhausted or self.hard_abort.is_set():
                    if isinstance(exc, httpx.TransportError):
                        raise ApiError(0, "network", f"{type(exc).__name__}: {exc}") from exc
                    raise
                delay = backoff.next()
                if isinstance(exc, ApiError) and exc.status == 429:
                    delay = max(delay, _retry_after(exc))
                log.warning(
                    "request failed, retrying",
                    extra={"fields": {"path": path, "error": str(exc), "retry_in_s": round(delay, 2)}},
                )
                if give_up_at is not None:
                    delay = max(0.0, min(delay, give_up_at - time.monotonic()))
                if interruptible and self.abort.wait(delay):
                    raise Aborted(f"{method} {path} aborted by shutdown") from exc
                if not interruptible and self.hard_abort.wait(delay):
                    raise Aborted(f"{method} {path} aborted by forced shutdown") from exc


#: Error codes of ``POST /jobs/{id}/fail`` (protocol v1).
ERROR_CODES = frozenset({"oom", "timeout", "safety", "bad_input", "canceled", "internal"})


def _api_error(resp: httpx.Response, path: str = "") -> ApiError:
    code, message = "http_error", resp.reason_phrase or "error"
    try:
        err = resp.json().get("error") or {}
        code = str(err.get("code") or code)
        message = str(err.get("message") or message)
    except (ValueError, AttributeError):
        text = resp.text.strip()
        if text:
            message = text[:300]
    if resp.status_code == 409 and code == "lease_lost":
        return LeaseLost(resp.status_code, code, message)
    if path.startswith("/jobs/") and resp.status_code in (403, 404, 409, 410):
        # 403: the job is leased to another worker; 404/410: the job no longer exists.
        return LeaseLost(resp.status_code, code, message)
    if resp.status_code in (401, 403):
        return Unauthorized(resp.status_code, code, message)
    if code in _UNKNOWN_WORKER_CODES or (resp.status_code == 404 and "worker" in message.lower()):
        return UnknownWorker(resp.status_code, code, message)
    exc = ApiError(resp.status_code, code, message)
    retry_after = resp.headers.get("retry-after")
    if retry_after:
        exc.retry_after = retry_after  # type: ignore[attr-defined]
    return exc


def _retry_after(exc: ApiError) -> float:
    raw = getattr(exc, "retry_after", None)
    try:
        return min(120.0, float(raw)) if raw else 0.0
    except ValueError:
        return 0.0
