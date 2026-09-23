"""Local model manifests.

The server's catalogue (``internal/ai/catalog/*.yaml``) describes models for routing. A worker additionally needs
to know *how* to run a model locally — which Hugging Face repo or file, which pipeline family, which ControlNet.
That lives in the ``worker:`` section of a manifest. The worker reads manifests from ``MV_MODELS_DIR`` (or the
packaged catalogue in ``mangaveyra_worker/catalog/``) and merges in the ``worker:`` section of the manifest the
server sends with each job (local files win, so a worker admin can point a model at a local path).

Example (``illustrious-xl.yaml``)::

    id: illustrious-xl
    name: Illustrious XL
    kind: image
    vram_mb: 10000        # comfortable VRAM, fully on the GPU
    min_vram_mb: 6000     # with model CPU offload
    worker:
      diffusers:
        repo: OnomaAIResearch/Illustrious-xl-early-release-v0
        family: sdxl
        controlnet_openpose: xinsir/controlnet-openpose-sdxl-1.0
        default_steps: 28
        default_guidance: 6.0
        trainable: true
"""

from __future__ import annotations

import json
import logging
from importlib import resources
from pathlib import Path
from typing import Any, Iterable, Mapping

import yaml

log = logging.getLogger(__name__)


def _deep_merge(base: Mapping[str, Any], over: Mapping[str, Any]) -> dict[str, Any]:
    out = dict(base)
    for key, value in over.items():
        if isinstance(value, Mapping) and isinstance(out.get(key), Mapping):
            out[key] = _deep_merge(out[key], value)
        else:
            out[key] = value
    return out


def load_manifest_file(path: Path) -> list[dict[str, Any]]:
    """Load one YAML/JSON manifest file (a single manifest, a list, or ``{"models": [...]}``)."""
    text = path.read_text(encoding="utf-8")
    doc = json.loads(text) if path.suffix == ".json" else yaml.safe_load(text)
    if doc is None:
        return []
    if isinstance(doc, Mapping) and "models" in doc and "id" not in doc:
        doc = doc["models"]
    items = doc if isinstance(doc, list) else [doc]
    out = []
    for item in items:
        if not isinstance(item, Mapping) or not item.get("id"):
            log.warning("skipping manifest without id", extra={"fields": {"file": str(path)}})
            continue
        out.append(dict(item))
    return out


class ModelCatalog:
    """Model manifests known to this worker, keyed by id."""

    def __init__(self, manifests: Iterable[Mapping[str, Any]] = ()) -> None:
        self._by_id: dict[str, dict[str, Any]] = {}
        for m in manifests:
            mid = str(m["id"])
            self._by_id[mid] = _deep_merge(self._by_id.get(mid, {}), m)

    @classmethod
    def load(cls, directory: Path | None) -> "ModelCatalog":
        """Load every ``*.yaml``/``*.yml``/``*.json`` in ``directory`` (the packaged catalogue when ``None``)."""
        manifests: list[dict[str, Any]] = []
        if directory is None:
            root = resources.files("mangaveyra_worker") / "catalog"
            paths = sorted(
                Path(str(p)) for p in root.iterdir() if p.name.endswith((".yaml", ".yml", ".json"))
            )
        else:
            if not directory.is_dir():
                log.warning("models dir does not exist", extra={"fields": {"dir": str(directory)}})
                return cls()
            paths = sorted(p for p in directory.iterdir() if p.suffix in (".yaml", ".yml", ".json"))
        for path in paths:
            try:
                manifests.extend(load_manifest_file(path))
            except (OSError, ValueError, yaml.YAMLError) as exc:
                log.warning("bad manifest", extra={"fields": {"file": str(path), "error": str(exc)}})
        return cls(manifests)

    def ids(self) -> list[str]:
        """All model ids."""
        return sorted(self._by_id)

    def get(self, model_id: str) -> dict[str, Any] | None:
        """The merged manifest for ``model_id``."""
        return self._by_id.get(model_id)

    def with_section(self, backend: str) -> list[dict[str, Any]]:
        """Manifests that carry a ``worker.<backend>`` section."""
        return [m for m in self._by_id.values() if isinstance((m.get("worker") or {}).get(backend), Mapping)]

    def resolve(
        self, model_id: str | None, backend: str, server_manifest: Mapping[str, Any] | None
    ) -> dict[str, Any]:
        """Return the effective ``worker.<backend>`` settings for a job (server section overlaid by local)."""
        remote = ((server_manifest or {}).get("worker") or {}).get(backend) or {}
        local = ((self.get(model_id) or {}).get("worker") or {}).get(backend) or {} if model_id else {}
        return _deep_merge(remote, local)

    def vram(self, model_id: str) -> tuple[int, int]:
        """``(vram_mb, min_vram_mb)`` for a model (0 when unknown)."""
        m = self.get(model_id) or {}
        vram = int(m.get("vram_mb") or 0)
        return vram, int(m.get("min_vram_mb") or vram)
