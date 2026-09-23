"""Resolve LoRA references of a job to local ``.safetensors`` files.

Jobs carry ``loras: [{"key": "...", "weight": 0.8}]`` where ``key`` is the server storage key of the LoRA (for
example the ``lora`` artifact of an earlier ``character.train`` job). A key is resolved, in order, from:

1. ``files["lora_<i>"]`` — a signed URL the server attaches for the i-th LoRA (downloaded once and cached under
   ``<data_dir>/loras/``; keys are immutable, so the cache never goes stale);
2. the local cache from an earlier download;
3. ``MV_LORA_DIR/<key>`` or ``MV_LORA_DIR/<basename(key)>`` — LoRAs the worker admin placed by hand.
"""

from __future__ import annotations

import hashlib
import os
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Mapping, Sequence

from mangaveyra_worker.errors import BadInput

_SAFE = re.compile(r"[^A-Za-z0-9_.-]+")


@dataclass(frozen=True)
class LoraRef:
    """A resolved LoRA: local path, weight and a stable adapter name."""

    key: str
    path: Path
    weight: float
    adapter: str


def _cache_name(key: str) -> str:
    digest = hashlib.sha256(key.encode()).hexdigest()[:16]
    stem = _SAFE.sub("_", Path(key).stem)[:40] or "lora"
    return f"{stem}-{digest}.safetensors"


def resolve_loras(
    specs: Sequence[Mapping[str, Any]] | None,
    files: Mapping[str, str],
    *,
    cache_dir: Path,
    lora_dir: Path | None,
    download: Callable[[str], bytes],
) -> list[LoraRef]:
    """Resolve every LoRA of a job or raise :class:`BadInput` naming the missing key."""
    refs: list[LoraRef] = []
    for i, spec in enumerate(specs or []):
        if not isinstance(spec, Mapping) or not spec.get("key"):
            raise BadInput(f"loras[{i}] needs a key")
        key = str(spec["key"])
        try:
            weight = float(spec.get("weight", 1.0))
        except (TypeError, ValueError) as exc:
            raise BadInput(f"loras[{i}].weight must be a number") from exc
        if not -4.0 <= weight <= 4.0:
            raise BadInput(f"loras[{i}].weight out of range")
        cached = cache_dir / _cache_name(key)
        path: Path | None = None
        if cached.is_file():
            path = cached
        elif f"lora_{i}" in files:
            cache_dir.mkdir(parents=True, exist_ok=True)
            data = download(files[f"lora_{i}"])
            tmp = cached.with_suffix(".part")
            tmp.write_bytes(data)
            os.replace(tmp, cached)
            path = cached
        elif lora_dir is not None:
            for candidate in (lora_dir / key, lora_dir / Path(key).name):
                resolved = candidate.resolve()
                if resolved.is_file() and lora_dir.resolve() in resolved.parents:
                    path = resolved
                    break
        if path is None:
            raise BadInput(
                f"LoRA {key!r} is not available on this worker (no lora_{i} file, not in MV_LORA_DIR)"
            )
        refs.append(
            LoraRef(
                key=key,
                path=path,
                weight=weight,
                adapter=f"mv{i}_{hashlib.sha1(key.encode()).hexdigest()[:8]}",
            )
        )
    return refs
