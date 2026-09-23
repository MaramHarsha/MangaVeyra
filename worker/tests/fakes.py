"""Test doubles: a fake MangaVeyra server speaking worker protocol v1 and a fake ComfyUI server."""

from __future__ import annotations

import io
import json
import threading
import time
from collections import defaultdict, deque
from dataclasses import dataclass, field
from email.parser import BytesParser
from email.policy import default as email_policy
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib.parse import parse_qs, urlparse

TOKEN = "mvw_test_token"


def parse_multipart(content_type: str, body: bytes) -> dict[str, tuple[bytes, str | None]]:
    """Parse multipart/form-data into ``{field: (data, filename)}``."""
    msg = BytesParser(policy=email_policy).parsebytes(
        b"Content-Type: " + content_type.encode() + b"\r\nMIME-Version: 1.0\r\n\r\n" + body
    )
    out: dict[str, tuple[bytes, str | None]] = {}
    for part in msg.iter_parts():
        name = part.get_param("name", header="content-disposition")
        out[str(name)] = (part.get_payload(decode=True) or b"", part.get_filename())
    return out


@dataclass
class JobRecord:
    """Server-side view of one job."""

    payload: dict[str, Any]
    progress: list[dict[str, Any]] = field(default_factory=list)
    artifacts: dict[str, dict[str, Any]] = field(default_factory=dict)
    result: dict[str, Any] | None = None
    error: dict[str, Any] | None = None
    done: threading.Event = field(default_factory=threading.Event)


class FakeMangaVeyra:
    """In-process fake of ``/api/worker/v1`` with fault injection hooks.

    * ``enqueue(job)`` adds a job; ``claim`` hands it out if its type was registered.
    * ``faults["/claim"] = deque([503, 503])`` answers the next requests to a path with those statuses.
    * ``cancel_after_progress[job_id] = n`` answers ``cancel: true`` from the n-th progress call on.
    * ``heartbeat_cancel`` ids are returned in the next heartbeat's ``cancel`` list.
    * ``lease_lost`` ids behave like the Go server after the lease moved on: progress answers ``cancel: true``,
      artifacts/complete/fail answer ``409 lease_lost``.
    * ``no_long_poll`` answers every empty claim with 204 at once (the Go server does this for draining workers).
    """

    def __init__(
        self, *, heartbeat_interval_s: float = 15, lease_s: int = 120, max_wait_s: float = 0.3
    ) -> None:
        self.heartbeat_interval_s = heartbeat_interval_s
        self.lease_s = lease_s
        self.max_wait_s = max_wait_s
        self.lock = threading.Lock()
        self.queue: deque[dict[str, Any]] = deque()
        self.jobs: dict[str, JobRecord] = {}
        self.registrations: list[dict[str, Any]] = []
        self.heartbeats: list[dict[str, Any]] = []
        self.claims: list[dict[str, Any]] = []
        self.requests: list[tuple[str, str]] = []
        self.faults: dict[str, deque[int]] = defaultdict(deque)
        self.cancel_after_progress: dict[str, int] = {}
        self.heartbeat_cancel: set[str] = set()
        self.lease_lost: set[str] = set()
        self.no_long_poll = False
        self.job_calls: list[tuple[str, str]] = []
        self.unknown_worker_once = False
        self.media: dict[str, bytes] = {}
        self.worker_types: list[str] = []
        self.worker_id_headers: set[str] = set()
        self._server = ThreadingHTTPServer(("127.0.0.1", 0), self._handler())
        self._server.daemon_threads = True
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)

    # ------------------------------------------------------------------ control

    @property
    def url(self) -> str:
        """Base URL."""
        host, port = self._server.server_address[:2]
        return f"http://{host}:{port}"

    def start(self) -> "FakeMangaVeyra":
        """Start serving."""
        self._thread.start()
        return self

    def stop(self) -> None:
        """Stop serving."""
        self._server.shutdown()
        self._server.server_close()

    def add_media(self, name: str, data: bytes) -> str:
        """Serve ``data`` at a signed-looking URL and return it."""
        self.media[name] = data
        return f"{self.url}/media/{name}?sig=test"

    def enqueue(self, job: dict[str, Any]) -> JobRecord:
        """Queue a job payload."""
        job.setdefault("attempt", 1)
        job.setdefault("lease_s", self.lease_s)
        job.setdefault("input", {})
        job.setdefault("files", {})
        rec = JobRecord(job)
        with self.lock:
            self.jobs[job["id"]] = rec
            self.queue.append(job)
        return rec

    def wait(self, job_id: str, timeout: float = 20.0) -> JobRecord:
        """Wait until the job completed or failed."""
        rec = self.jobs[job_id]
        assert rec.done.wait(timeout), f"job {job_id} did not finish (progress={rec.progress[-3:]})"
        return rec

    # ------------------------------------------------------------------ HTTP

    def _handler(self) -> type[BaseHTTPRequestHandler]:
        server = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args: Any) -> None:  # silence
                pass

            def _send(
                self, status: int, body: Any = None, raw: bytes | None = None, ctype: str = "application/json"
            ) -> None:
                data = raw if raw is not None else (b"" if body is None else json.dumps(body).encode())
                self.send_response(status)
                if status != 204:
                    self.send_header("Content-Type", ctype)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                if data:
                    self.wfile.write(data)

            def _error(self, status: int, code: str, message: str) -> None:
                self._send(status, {"error": {"code": code, "message": message}})

            def do_GET(self) -> None:  # noqa: N802
                path = urlparse(self.path).path
                server.requests.append(("GET", path))
                if path.startswith("/media/"):
                    name = path.removeprefix("/media/")
                    if name not in server.media:
                        return self._error(404, "not_found", "no such media")
                    return self._send(200, raw=server.media[name], ctype="application/octet-stream")
                self._error(404, "not_found", path)

            def do_POST(self) -> None:  # noqa: N802
                path = urlparse(self.path).path
                length = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(length) if length else b""
                server.requests.append(("POST", path))
                if self.headers.get("Authorization") != f"Bearer {TOKEN}":
                    return self._error(401, "unauthorized", "bad worker token")
                prefix = "/api/worker/v1"
                if not path.startswith(prefix):
                    return self._error(404, "not_found", path)
                route = path.removeprefix(prefix)
                fault_key = route if not route.startswith("/jobs/") else "/jobs/*/" + route.rsplit("/", 1)[-1]
                with server.lock:
                    fault = server.faults[fault_key].popleft() if server.faults[fault_key] else None
                if fault is not None:
                    return self._error(fault, "injected", f"injected {fault}")
                if route.startswith("/jobs/"):
                    _, _, job_id, action = route.split("/", 3)
                    return self._job(job_id, action, body)
                doc = json.loads(body or b"{}")
                if route == "/register":
                    server.registrations.append(doc)
                    server.worker_types = list(doc.get("job_types") or [])
                    return self._send(
                        200,
                        {
                            "worker_id": "01JWORKER",
                            "heartbeat_interval_s": server.heartbeat_interval_s,
                            "lease_s": server.lease_s,
                        },
                    )
                if route == "/heartbeat":
                    server.heartbeats.append(doc)
                    with server.lock:
                        cancel = sorted(server.heartbeat_cancel & set(doc.get("running") or []))
                        server.heartbeat_cancel -= set(cancel)
                    return self._send(200, {"cancel": cancel})
                if route == "/claim":
                    server.claims.append(doc)
                    if server.unknown_worker_once:
                        server.unknown_worker_once = False
                        return self._error(404, "unknown_worker", "worker not registered")
                    wait = 0.0 if server.no_long_poll else min(float(doc.get("wait_s") or 0), server.max_wait_s)
                    deadline = time.monotonic() + wait
                    while True:
                        with server.lock:
                            for i, job in enumerate(server.queue):
                                if job["type"] in (doc.get("job_types") or []):
                                    del server.queue[i]
                                    return self._send(200, {"job": job})
                        if time.monotonic() >= deadline:
                            return self._send(204)
                        time.sleep(0.02)
                self._error(404, "not_found", route)

            def _job(self, job_id: str, action: str, body: bytes) -> None:
                rec = server.jobs.get(job_id)
                if rec is None:
                    return self._error(404, "not_found", "no such job")
                wid = self.headers.get("X-Worker-Id")
                if wid is not None:
                    server.worker_id_headers.add(wid)
                server.job_calls.append((job_id, action))
                if job_id in server.lease_lost:
                    if action == "progress":
                        return self._send(200, {"cancel": True})
                    return self._error(409, "lease_lost", "this job is no longer leased to the worker; drop it")
                if action == "progress":
                    doc = json.loads(body)
                    rec.progress.append(doc)
                    limit = server.cancel_after_progress.get(job_id)
                    return self._send(200, {"cancel": bool(limit is not None and len(rec.progress) >= limit)})
                if action == "artifacts":
                    parts = parse_multipart(self.headers["Content-Type"], body)
                    name = parts["name"][0].decode()
                    meta = json.loads(parts["meta"][0]) if "meta" in parts else None
                    key = f"jobs/{job_id}/{name}"
                    rec.artifacts[key] = {
                        "data": parts["file"][0],
                        "filename": parts["file"][1],
                        "meta": meta,
                    }
                    return self._send(200, {"key": key})
                if action == "complete":
                    rec.result = json.loads(body)
                    rec.done.set()
                    return self._send(200, {"ok": True})
                if action == "fail":
                    rec.error = json.loads(body)["error"]
                    rec.done.set()
                    return self._send(200, {"ok": True})
                self._error(404, "not_found", action)

        return Handler


class FakeComfyUI:
    """A fake ComfyUI HTTP API: /system_stats, /upload/image, /prompt, /history, /view, /queue, /interrupt.

    Every queued prompt "renders" instantly: its output is a solid PNG whose colour encodes the seed found in
    the workflow, so tests can check the input mapping end to end. No websocket (the backend falls back to polling).
    """

    def __init__(self, *, output_node: str = "9", fail_with: str | None = None, delay_s: float = 0.0) -> None:
        self.output_node = output_node
        self.fail_with = fail_with
        self.delay_s = delay_s
        self.uploads: dict[str, bytes] = {}
        self.prompts: dict[str, dict[str, Any]] = {}
        self.finished_at: dict[str, float] = {}
        self.deleted: list[str] = []
        self.interrupted = 0
        self._n = 0
        self._server = ThreadingHTTPServer(("127.0.0.1", 0), self._handler())
        self._server.daemon_threads = True
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)

    @property
    def url(self) -> str:
        """Base URL."""
        host, port = self._server.server_address[:2]
        return f"http://{host}:{port}"

    def start(self) -> "FakeComfyUI":
        """Start serving."""
        self._thread.start()
        return self

    def stop(self) -> None:
        """Stop serving."""
        self._server.shutdown()
        self._server.server_close()

    def _render(self, workflow: dict[str, Any]) -> bytes:
        from PIL import Image

        seed = 0
        for node in workflow.values():
            if isinstance(node, dict) and "seed" in node.get("inputs", {}):
                seed = int(node["inputs"]["seed"])
        img = Image.new("RGB", (64, 48), (seed % 256, (seed // 256) % 256, 7))
        buf = io.BytesIO()
        img.save(buf, format="PNG")
        return buf.getvalue()

    def _handler(self) -> type[BaseHTTPRequestHandler]:
        comfy = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args: Any) -> None:
                pass

            def _send(
                self, status: int, body: Any = None, raw: bytes | None = None, ctype: str = "application/json"
            ) -> None:
                data = raw if raw is not None else json.dumps(body if body is not None else {}).encode()
                self.send_response(status)
                self.send_header("Content-Type", ctype)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_GET(self) -> None:  # noqa: N802
                url = urlparse(self.path)
                if url.path == "/system_stats":
                    return self._send(200, {"system": {"comfyui_version": "fake"}})
                if url.path.startswith("/history/"):
                    pid = url.path.rsplit("/", 1)[-1]
                    if pid not in comfy.prompts or time.monotonic() < comfy.finished_at[pid]:
                        return self._send(200, {})
                    if comfy.fail_with:
                        entry = {
                            "status": {
                                "status_str": "error",
                                "completed": False,
                                "messages": [["execution_error", {"exception_message": comfy.fail_with}]],
                            },
                            "outputs": {},
                        }
                    else:
                        entry = {
                            "status": {"status_str": "success", "completed": True},
                            "outputs": {
                                comfy.output_node: {
                                    "images": [{"filename": f"{pid}.png", "subfolder": "", "type": "output"}]
                                }
                            },
                        }
                    return self._send(200, {pid: entry})
                if url.path == "/view":
                    q = parse_qs(url.query)
                    pid = q["filename"][0].removesuffix(".png")
                    return self._send(200, raw=comfy._render(comfy.prompts[pid]), ctype="image/png")
                if url.path == "/queue":
                    now = time.monotonic()
                    pending = [[0, pid, {}, {}, []] for pid, t in comfy.finished_at.items() if t > now]
                    return self._send(200, {"queue_running": [], "queue_pending": pending})
                self._send(404, {"error": "not found"})

            def do_POST(self) -> None:  # noqa: N802
                url = urlparse(self.path)
                length = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(length) if length else b""
                if url.path == "/upload/image":
                    parts = parse_multipart(self.headers["Content-Type"], body)
                    data, filename = parts["image"]
                    comfy.uploads[filename or "upload.png"] = data
                    return self._send(200, {"name": filename, "subfolder": "", "type": "input"})
                if url.path == "/prompt":
                    doc = json.loads(body)
                    wf = doc["prompt"]
                    for node in wf.values():
                        if node.get("class_type") == "Broken":
                            return self._send(
                                400,
                                {"error": {"message": "Prompt outputs failed validation"}, "node_errors": {}},
                            )
                    comfy._n += 1
                    pid = f"p{comfy._n}"
                    comfy.prompts[pid] = wf
                    comfy.finished_at[pid] = time.monotonic() + comfy.delay_s
                    return self._send(200, {"prompt_id": pid, "number": comfy._n})
                if url.path == "/queue":
                    doc = json.loads(body)
                    comfy.deleted.extend(doc.get("delete") or [])
                    for pid in doc.get("delete") or []:
                        comfy.finished_at[pid] = float("inf")
                    return self._send(200, {})
                if url.path == "/interrupt":
                    comfy.interrupted += 1
                    return self._send(200, {})
                self._send(404, {"error": "not found"})

        return Handler
