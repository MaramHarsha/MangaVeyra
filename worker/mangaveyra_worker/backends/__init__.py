"""Backend registry.

Backends are imported lazily by name so that a worker running only ``mock`` never imports torch, OpenCV or the
ComfyUI client.
"""

from __future__ import annotations

import importlib
import logging
from typing import TYPE_CHECKING, Any

from mangaveyra_worker.config import KNOWN_BACKENDS
from mangaveyra_worker.errors import BackendUnavailable, ConfigError

if TYPE_CHECKING:  # pragma: no cover
    from mangaveyra_worker.backends.base import Backend
    from mangaveyra_worker.config import Config

log = logging.getLogger(__name__)

#: name → "module:Class"
REGISTRY: dict[str, str] = {
    "diffusers": "mangaveyra_worker.backends.diffusers_backend:DiffusersBackend",
    "comfyui": "mangaveyra_worker.backends.comfyui:ComfyUIBackend",
    "vision": "mangaveyra_worker.backends.vision:VisionBackend",
    "mock": "mangaveyra_worker.backends.mock:MockBackend",
}

#: Job types that produce artwork; ``auto`` adds the mock only when no real backend covers them.
_ART_TYPES = {"image.generate", "image.edit", "image.inpaint", "sketch.ink", "character.sheet"}
#: Mock job types whose results are real (not placeholders).
_HONEST_MOCK_TYPES = {"detect.panels", "upscale"}


def backend_class(name: str) -> type["Backend"]:
    """Import and return the backend class registered under ``name``."""
    try:
        target = REGISTRY[name]
    except KeyError as exc:
        raise ConfigError(f"unknown backend {name!r}") from exc
    module, _, cls = target.partition(":")
    try:
        return getattr(importlib.import_module(module), cls)
    except ImportError as exc:
        raise BackendUnavailable(f"{name}: {exc}") from exc


def build_backends(config: "Config", gpus: list[dict[str, Any]]) -> list["Backend"]:
    """Instantiate the configured backends in dispatch order.

    Explicit names must be available (a clear error beats a worker that silently serves placeholders). ``auto``
    takes every available real backend that actually serves something, then adds ``mock``: the full mock when no
    real backend can produce artwork (so a fresh install completes every flow), otherwise only the mock's *honest*
    helpers (real panel detection and Lanczos upscaling) for types nothing else serves — a GPU install never gets
    placeholder art, fake Character Lock scores or placeholder OCR text by accident.
    """
    names = config.backends or ["auto"]
    auto = "auto" in names
    wanted = list(KNOWN_BACKENDS) if auto else [n for n in names if n != "auto"]
    backends: list[Backend] = []
    for name in wanted:
        if auto and name == "mock":
            continue
        try:
            cls = backend_class(name)
            ok, reason = cls.available(config)
        except BackendUnavailable as exc:
            ok, reason = False, str(exc)
        if not ok:
            if auto:
                log.info("backend not available", extra={"fields": {"backend": name, "reason": reason}})
                continue
            raise BackendUnavailable(f"backend {name!r} is not available: {reason}")
        backend = cls(config, gpus)
        if auto and not backend.job_types():
            log.info("backend has nothing to serve", extra={"fields": {"backend": name}})
            backend.close()
            continue
        backends.append(backend)
    if auto:
        served = {t for b in backends for t in b.job_types()}
        mock_cls: Any = backend_class("mock")
        if not _ART_TYPES & served:
            backends.append(mock_cls(config, gpus))
        elif missing := _HONEST_MOCK_TYPES - served:
            backends.append(mock_cls(config, gpus, only=missing))
    if not backends:
        raise ConfigError("no backend available")
    return backends
