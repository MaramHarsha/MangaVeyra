"""Job model and the per-job context handed to backends.

A backend receives a :class:`Job` (the decoded ``claim`` payload) and a :class:`JobContext`, which gives it
everything it may do while running: read input files, report progress (and learn about cancellation), upload
artifacts and use a private scratch directory. The backend returns a :class:`JobResult` whose ``output`` follows
the per-type table in ``sdk/api/worker.md``.
"""

from __future__ import annotations

import base64
import io
import logging
import re
import shutil
import tempfile
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING, Any, Mapping

from PIL import Image, ImageOps

from mangaveyra_worker.errors import ApiError, BadInput, JobCanceled
from mangaveyra_worker.log import bind

if TYPE_CHECKING:  # pragma: no cover
    from mangaveyra_worker.client import ServerClient

log = logging.getLogger(__name__)

#: Refuse to decode images above this many pixels (decompression-bomb guard; 8K × 8K).
MAX_IMAGE_PIXELS = 8192 * 8192
#: Protocol limit for inline progress previews.
MAX_PREVIEW_BYTES = 64 * 1024

#: Every job type in protocol v1.
ALL_JOB_TYPES = (
    "image.generate",
    "image.edit",
    "image.inpaint",
    "sketch.ink",
    "character.sheet",
    "character.train",
    "character.score",
    "detect.panels",
    "ocr",
    "upscale",
    "video.animate",
    "voice.tts",
)

#: Job types whose output is ``{"images": [...]}``.
IMAGE_JOB_TYPES = (
    "image.generate",
    "image.edit",
    "image.inpaint",
    "sketch.ink",
    "character.sheet",
    "upscale",
)


@dataclass(frozen=True)
class ModelRef:
    """The model the router picked for this job: its id and the full manifest from the server catalogue."""

    id: str | None
    manifest: Mapping[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class Job:
    """One claimed job."""

    id: str
    type: str
    attempt: int = 1
    lease_s: int = 120
    model: ModelRef = field(default_factory=lambda: ModelRef(None))
    input: Mapping[str, Any] = field(default_factory=dict)
    files: Mapping[str, str] = field(default_factory=dict)

    @classmethod
    def from_payload(cls, data: Mapping[str, Any]) -> "Job":
        """Decode the ``job`` object of a ``claim`` response."""
        if not isinstance(data, Mapping):
            raise ValueError("job payload must be an object")
        if not data.get("id") or not data.get("type"):
            raise ValueError("job payload needs id and type")
        model = data.get("model") or {}
        inp = data.get("input") or {}
        files = data.get("files") or {}
        for name, value in (("model", model), ("input", inp), ("files", files)):
            if not isinstance(value, Mapping):
                raise ValueError(f"job {name} must be an object")
        manifest = model.get("manifest") or {}
        # The server sends "id": "" for jobs without a model (any model / model-less types); treat it as no model.
        model_id = model.get("id")
        return cls(
            id=str(data["id"]),
            type=str(data["type"]),
            attempt=int(data.get("attempt") or 1),
            lease_s=max(1, int(data.get("lease_s") or 120)),
            model=ModelRef(
                str(model_id) if model_id else None, manifest if isinstance(manifest, Mapping) else {}
            ),
            input=dict(inp),
            files={str(k): str(v) for k, v in files.items()},
        )

    @property
    def model_id(self) -> str | None:
        """Shortcut for ``job.model.id``."""
        return self.model.id


@dataclass
class JobResult:
    """What a backend returns: the protocol ``output`` object plus accounting for ``actual``."""

    output: dict[str, Any]
    gpu_seconds: float = 0.0


def indexed_names(files: Mapping[str, str], prefix: str) -> list[str]:
    """Return ``prefix0, prefix1, …`` file names present in ``files`` in numeric order.

    ``indexed_names(files, "ref_")`` → ``["ref_0", "ref_1", "ref_10"]`` (numeric, not lexical, order).
    """
    pat = re.compile(re.escape(prefix) + r"(\d+)$")
    found = [(int(m.group(1)), name) for name in files if (m := pat.match(name))]
    return [name for _, name in sorted(found)]


def decode_image(data: bytes, *, name: str = "image", mode: str | None = "RGB") -> Image.Image:
    """Decode image bytes safely (size limit, EXIF orientation applied) or raise :class:`BadInput`."""
    try:
        img = Image.open(io.BytesIO(data))
        if img.width * img.height > MAX_IMAGE_PIXELS:
            raise BadInput(f"{name}: image too large ({img.width}×{img.height})")
        img.load()
        img = ImageOps.exif_transpose(img) or img
    except BadInput:
        raise
    except Exception as exc:  # Pillow raises many types for broken files
        raise BadInput(f"{name}: not a readable image ({exc})") from exc
    if mode and img.mode != mode:
        if mode == "RGB" and img.mode in ("RGBA", "LA", "P"):
            # Flatten transparency onto white paper rather than black.
            rgba = img.convert("RGBA")
            bg = Image.new("RGB", rgba.size, (255, 255, 255))
            bg.paste(rgba, mask=rgba.getchannel("A"))
            img = bg
        else:
            img = img.convert(mode)
    return img


def encode_png(img: Image.Image, *, text: Mapping[str, str] | None = None) -> bytes:
    """Encode a PNG deterministically (no timestamps), optionally with ``tEXt`` chunks."""
    from PIL import PngImagePlugin

    info = PngImagePlugin.PngInfo()
    for key, value in (text or {}).items():
        info.add_text(key, value)
    buf = io.BytesIO()
    img.save(buf, format="PNG", pnginfo=info, optimize=False, compress_level=6)
    return buf.getvalue()


def preview_b64(img: Image.Image, limit: int = MAX_PREVIEW_BYTES) -> str | None:
    """Return a base64 PNG thumbnail no larger than ``limit`` bytes (or ``None`` if impossible)."""
    thumb = img.convert("RGB") if img.mode not in ("RGB", "L") else img.copy()
    side = 256
    while side >= 32:
        t = thumb.copy()
        t.thumbnail((side, side), Image.Resampling.BILINEAR)
        buf = io.BytesIO()
        t.save(buf, format="PNG", optimize=True)
        if buf.tell() <= limit * 3 // 4:  # base64 grows by 4/3
            return base64.b64encode(buf.getvalue()).decode("ascii")
        side //= 2
    return None


class JobContext:
    """Everything a backend may do while running one job. Thread-safe.

    Cancellation is cooperative: backends call :meth:`check` (or :meth:`progress`, which checks) between steps;
    both raise :class:`JobCanceled` once the server, a heartbeat or a shutdown asked the job to stop.
    """

    def __init__(
        self,
        job: Job,
        client: "ServerClient",
        *,
        scratch_root: Path | None = None,
        progress_interval: float = 0.5,
    ) -> None:
        self.job = job
        self.client = client
        self.log = bind(log, job=job.id, type=job.type)
        self.cancel_event = threading.Event()
        self.cancel_reason: str | None = None
        self.started = time.monotonic()
        self.last_contact = time.monotonic()
        self._progress_interval = progress_interval
        self._lock = threading.Lock()
        self._last_sent = 0.0
        self._state: dict[str, Any] = {"progress": 0.0}
        self._files: dict[str, bytes] = {}
        self._uploads: list[str] = []
        scratch_root = Path(scratch_root) if scratch_root else None
        if scratch_root:
            scratch_root.mkdir(parents=True, exist_ok=True)
        self.scratch = Path(tempfile.mkdtemp(prefix=f"job-{job.id[-8:]}-", dir=scratch_root))

    # ------------------------------------------------------------------ cancellation

    def cancel(self, reason: str = "server") -> None:
        """Ask the job to stop (``reason`` is ``server``, ``heartbeat`` or ``shutdown``)."""
        if not self.cancel_event.is_set():
            self.cancel_reason = reason
            self.cancel_event.set()

    @property
    def canceled(self) -> bool:
        """Whether the job was asked to stop."""
        return self.cancel_event.is_set()

    def check(self) -> None:
        """Raise :class:`JobCanceled` if the job was asked to stop."""
        if self.cancel_event.is_set():
            raise JobCanceled(f"job canceled ({self.cancel_reason})")

    # ------------------------------------------------------------------ progress

    def progress(
        self,
        fraction: float,
        *,
        step: int | None = None,
        steps: int | None = None,
        message: str | None = None,
        preview: Image.Image | None = None,
        force: bool = False,
    ) -> None:
        """Report progress (throttled to one request per ``progress_interval`` unless ``force``).

        Also checks cancellation, so calling it once per denoising step is enough for cooperative cancel.
        """
        self.check()
        with self._lock:
            self._state.update(progress=fraction, step=step, steps=steps, message=message)
            now = time.monotonic()
            due = force or fraction >= 1.0 or now - self._last_sent >= self._progress_interval
            if not due:
                return
            self._last_sent = now
        preview_data = preview_b64(preview) if preview is not None else None
        self._send(preview_data)
        self.check()

    def keepalive(self) -> None:
        """Resend the last progress to extend the lease (called by the runner's lease thread)."""
        with self._lock:
            self._last_sent = time.monotonic()
        self._send(None)

    def _send(self, preview: str | None) -> None:
        state = dict(self._state)
        try:
            stop = self.client.progress(
                self.job.id,
                state.get("progress") or 0.0,
                step=state.get("step"),
                steps=state.get("steps"),
                message=state.get("message"),
                preview_png_b64=preview,
            )
        except ApiError as exc:
            self.log.warning("progress not delivered", extra={"fields": {"error": str(exc)}})
            return
        self.last_contact = time.monotonic()
        if stop:
            self.cancel("server")

    # ------------------------------------------------------------------ inputs

    def has_file(self, name: str) -> bool:
        """Whether the job carries an input file called ``name``."""
        return name in self.job.files

    def file_names(self, prefix: str) -> list[str]:
        """Indexed input names such as ``ref_0…ref_n`` in numeric order."""
        return indexed_names(self.job.files, prefix)

    def file_bytes(self, name: str) -> bytes:
        """Download (once) and return the bytes of input file ``name``."""
        if name not in self.job.files:
            raise BadInput(f"missing input file {name!r}")
        with self._lock:
            cached = self._files.get(name)
        if cached is not None:
            return cached
        self.check()
        data = self.client.download(self.job.files[name])
        with self._lock:
            self._files[name] = data
        return data

    def image(self, name: str, mode: str | None = "RGB") -> Image.Image:
        """Download and decode input image ``name``."""
        return decode_image(self.file_bytes(name), name=name, mode=mode)

    def images(self, prefix: str, mode: str | None = "RGB") -> list[Image.Image]:
        """All indexed images with ``prefix`` (``ref_``, ``reference_``, ``candidate_``…)."""
        return [self.image(n, mode) for n in self.file_names(prefix)]

    def save_input(self, name: str, suffix: str = "") -> Path:
        """Write input file ``name`` into the scratch dir and return its path."""
        path = self.scratch / f"{name}{suffix}"
        path.write_bytes(self.file_bytes(name))
        return path

    # ------------------------------------------------------------------ outputs

    def upload(
        self,
        name: str,
        data: bytes,
        *,
        content_type: str = "image/png",
        filename: str | None = None,
        meta: Mapping[str, Any] | None = None,
    ) -> str:
        """Upload an output file and return its storage key."""
        self.check()
        key = self.client.upload_artifact(
            self.job.id, name, data, filename=filename, content_type=content_type, meta=meta
        )
        self.last_contact = time.monotonic()
        self._uploads.append(key)
        return key

    def upload_image(self, name: str, img: Image.Image, *, meta: Mapping[str, Any] | None = None) -> str:
        """Encode ``img`` as PNG and upload it as ``name``."""
        text = {"Software": "MangaVeyra worker"}
        return self.upload(name, encode_png(img, text=text), filename=f"{name}.png", meta=meta)

    @property
    def uploaded(self) -> list[str]:
        """Keys uploaded so far."""
        return list(self._uploads)

    def cleanup(self) -> None:
        """Delete the scratch directory."""
        shutil.rmtree(self.scratch, ignore_errors=True)


def clamp_int(value: Any, default: int, lo: int, hi: int, name: str) -> int:
    """Parse an integer input field with a default and bounds, or raise :class:`BadInput`."""
    if value is None or value == "":
        return default
    try:
        v = int(value)
    except (TypeError, ValueError) as exc:
        raise BadInput(f"{name} must be an integer") from exc
    if not lo <= v <= hi:
        raise BadInput(f"{name} must be between {lo} and {hi}")
    return v


def clamp_float(value: Any, default: float, lo: float, hi: float, name: str) -> float:
    """Parse a float input field with a default and bounds, or raise :class:`BadInput`."""
    if value is None or value == "":
        return default
    try:
        v = float(value)
    except (TypeError, ValueError) as exc:
        raise BadInput(f"{name} must be a number") from exc
    if not lo <= v <= hi:
        raise BadInput(f"{name} must be between {lo} and {hi}")
    return v


def validate_trigger(value: Any) -> str:
    """Return a LoRA trigger word or raise :class:`BadInput`.

    1–64 characters of letters, digits, spaces and ``_ - . '``, starting with a letter or digit. Triggers end up in
    prompts, file names and (with ``MV_TRAIN_COMMAND``) trainer arguments, where a leading ``-`` would be read as a
    flag — so anything else is refused.
    """
    trigger = str(value or "").strip()
    if not trigger:
        raise BadInput("trigger is required")
    if len(trigger) > 64 or not trigger[0].isalnum():
        raise BadInput("trigger must be 1–64 characters and start with a letter or digit")
    if any(not (c.isalnum() or c in " _-.'") for c in trigger):
        raise BadInput("trigger may only contain letters, digits, spaces and _ - . '")
    return trigger


def resolve_seed(value: Any) -> int:
    """Seeds are integers; ``null`` means random (a fresh 32-bit seed is drawn and reported back)."""
    if value is None or value == "":
        import secrets

        return secrets.randbelow(2**32)
    try:
        return int(value) % (2**32)
    except (TypeError, ValueError) as exc:
        raise BadInput("seed must be an integer or null") from exc
