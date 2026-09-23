"""VRAM-aware cache of diffusers pipelines.

Loading an SDXL or FLUX pipeline takes 5–60 s, so pipelines stay resident between jobs. Every cached entry carries
an estimated VRAM cost (manifest ``vram_mb``, or ``min_vram_mb`` when CPU offload is used); loading a new model
evicts the least-recently-used entries until the estimate fits the budget (``MV_VRAM_BUDGET_MB``, default 90 % of
the GPU). Derived pipelines (img2img/inpaint/ControlNet built with ``from_pipe``) share the base weights and cost
nothing extra; separately loaded pipelines (instruct edit, dedicated inpainting checkpoints, ControlNets) are
accounted on their own.

Pipelines are not thread-safe: every entry has a lock the backend holds for the duration of one inference. Use
:meth:`PipelineCache.checkout`, which pins the entry in the same critical section that finds (or loads) it, so a
concurrent load can never evict a pipeline between the lookup and the moment its user takes the lock.
"""

from __future__ import annotations

import gc
import logging
import threading
import time
from collections import OrderedDict
from contextlib import contextmanager
from dataclasses import dataclass, field
from typing import Any, Callable, Iterator

log = logging.getLogger(__name__)


@dataclass
class Entry:
    """One cached pipeline."""

    key: str
    pipe: Any
    vram_mb: int
    lock: threading.Lock = field(default_factory=threading.Lock)
    last_used: float = field(default_factory=time.monotonic)
    derived: dict[str, Any] = field(default_factory=dict)
    #: Jobs that checked the entry out (waiting for or holding ``lock``); pinned entries are never evicted.
    pins: int = 0


class PipelineCache:
    """LRU cache keyed by string, bounded by estimated VRAM."""

    def __init__(self, budget_mb: int, *, on_evict: Callable[[Any], None] | None = None) -> None:
        self.budget_mb = budget_mb
        self._entries: OrderedDict[str, Entry] = OrderedDict()
        self._lock = threading.Lock()
        self._loading: dict[str, threading.Lock] = {}
        self._on_evict = on_evict

    @property
    def used_mb(self) -> int:
        """Sum of the resident estimates."""
        return sum(e.vram_mb for e in self._entries.values())

    def keys(self) -> list[str]:
        """Resident keys, least recently used first."""
        return list(self._entries)

    @contextmanager
    def checkout(self, key: str, vram_mb: int, loader: Callable[[], Any]) -> Iterator[Entry]:
        """Pin the entry for ``key`` (loading it when missing), hold its lock for the ``with`` body, then unpin.

        This is how backends use the cache: ``with cache.checkout(key, mb, load) as entry: entry.pipe(...)``.
        """
        entry = self.get(key, vram_mb, loader, pin=True)
        try:
            with entry.lock:
                entry.last_used = time.monotonic()
                yield entry
        finally:
            with self._lock:
                entry.pins -= 1

    def get(self, key: str, vram_mb: int, loader: Callable[[], Any], *, pin: bool = False) -> Entry:
        """Return the cached entry for ``key``, loading it with ``loader`` (after evictions) when missing.

        With ``pin`` the entry's pin count is raised atomically with the lookup; the caller must lower it again
        (prefer :meth:`checkout`, which does both).
        """
        with self._lock:
            entry = self._entries.get(key)
            if entry is not None:
                self._entries.move_to_end(key)
                entry.last_used = time.monotonic()
                entry.pins += int(pin)
                return entry
            load_lock = self._loading.setdefault(key, threading.Lock())
        with load_lock:  # one loader per key; other keys may load concurrently
            with self._lock:
                entry = self._entries.get(key)
                if entry is not None:
                    entry.pins += int(pin)
                    return entry
                self._make_room(vram_mb)
            started = time.monotonic()
            pipe = loader()
            entry = Entry(key=key, pipe=pipe, vram_mb=vram_mb, pins=int(pin))
            with self._lock:
                self._entries[key] = entry
            log.info(
                "pipeline loaded",
                extra={
                    "fields": {
                        "key": key,
                        "vram_mb": vram_mb,
                        "seconds": round(time.monotonic() - started, 1),
                        "cache_mb": self.used_mb,
                    }
                },
            )
            return entry

    def _make_room(self, need_mb: int) -> None:
        """Evict idle LRU entries until ``need_mb`` fits (caller holds ``self._lock``)."""
        for key in list(self._entries):
            if self.used_mb + need_mb <= self.budget_mb:
                break
            entry = self._entries[key]
            if entry.pins > 0 or not entry.lock.acquire(blocking=False):
                continue  # checked out or in use by a running job; never evict under it
            try:
                self._evict(key)
            finally:
                entry.lock.release()

    def _evict(self, key: str) -> None:
        entry = self._entries.pop(key)
        log.info("pipeline evicted", extra={"fields": {"key": key, "vram_mb": entry.vram_mb}})
        if self._on_evict is not None:
            self._on_evict(entry.pipe)
        entry.derived.clear()
        del entry
        gc.collect()
        free_accelerator()

    def clear(self) -> None:
        """Drop every idle pipeline (used before LoRA training, which needs the whole GPU, and on shutdown).

        Entries checked out by a running job stay: evicting them would free nothing (the job still holds the
        pipeline) and would only break the accounting.
        """
        with self._lock:
            for key in list(self._entries):
                entry = self._entries[key]
                if entry.pins > 0 or not entry.lock.acquire(blocking=False):
                    log.info("pipeline in use; not cleared", extra={"fields": {"key": key}})
                    continue
                try:
                    self._evict(key)
                finally:
                    entry.lock.release()


def free_accelerator() -> None:
    """Release cached CUDA/MPS memory if torch is loaded."""
    import sys

    torch = sys.modules.get("torch")
    if torch is None:
        return
    try:
        if torch.cuda.is_available():
            torch.cuda.empty_cache()
        elif getattr(torch.backends, "mps", None) and torch.backends.mps.is_available():
            torch.mps.empty_cache()
    except Exception:  # pragma: no cover - driver specific
        pass
