"""GPU detection for registration and heartbeats.

Prefers ``torch.cuda`` (exact free memory per device) when PyTorch is installed and imported cheaply; otherwise
parses ``nvidia-smi``. Returns ``[]`` on machines without an NVIDIA GPU — the mock and vision backends do not
need one.
"""

from __future__ import annotations

import logging
import shutil
import subprocess
import sys
from typing import Any

log = logging.getLogger(__name__)

_SMI_QUERY = ["--query-gpu=index,name,memory.total,memory.free", "--format=csv,noheader,nounits"]


def parse_nvidia_smi(text: str) -> list[dict[str, Any]]:
    """Parse ``nvidia-smi --query-gpu=index,name,memory.total,memory.free --format=csv,noheader,nounits``.

    >>> parse_nvidia_smi("0, NVIDIA GeForce RTX 4090, 24564, 20110")
    [{'index': 0, 'name': 'NVIDIA GeForce RTX 4090', 'vram_total_mb': 24564, 'vram_free_mb': 20110}]
    """
    gpus: list[dict[str, Any]] = []
    for line in text.splitlines():
        parts = [p.strip() for p in line.split(",")]
        if len(parts) < 4:
            continue
        index, total, free = parts[0], parts[-2], parts[-1]
        name = ",".join(parts[1:-2]).strip()
        try:
            gpus.append(
                {
                    "index": int(index),
                    "name": name,
                    "vram_total_mb": int(float(total)),
                    "vram_free_mb": int(float(free)),
                }
            )
        except ValueError:
            continue  # "[N/A]" on some virtualised GPUs
    return gpus


def _from_torch() -> list[dict[str, Any]] | None:
    torch = sys.modules.get("torch")
    if torch is None:
        return None  # never import torch just to probe — it costs seconds and ~1 GB of RAM
    try:
        if not torch.cuda.is_available():
            return []
        gpus = []
        for i in range(torch.cuda.device_count()):
            free, total = torch.cuda.mem_get_info(i)
            gpus.append(
                {
                    "index": i,
                    "name": torch.cuda.get_device_name(i),
                    "vram_total_mb": total // (1024 * 1024),
                    "vram_free_mb": free // (1024 * 1024),
                }
            )
        return gpus
    except Exception as exc:  # pragma: no cover - driver specific
        log.debug("torch GPU probe failed: %s", exc)
        return None


def _from_nvidia_smi() -> list[dict[str, Any]]:
    exe = shutil.which("nvidia-smi")
    if not exe:
        return []
    try:
        out = subprocess.run([exe, *_SMI_QUERY], capture_output=True, text=True, timeout=10, check=True)
    except (OSError, subprocess.SubprocessError) as exc:
        log.warning("nvidia-smi failed: %s", exc)
        return []
    return parse_nvidia_smi(out.stdout)


def detect_gpus() -> list[dict[str, Any]]:
    """Return ``[{"index", "name", "vram_total_mb", "vram_free_mb"}, …]`` for every visible NVIDIA GPU."""
    gpus = _from_torch()
    if gpus is None:
        gpus = _from_nvidia_smi()
    return gpus


def protocol_gpus(gpus: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Strip local-only keys to the protocol shape ``{"name", "vram_total_mb", "vram_free_mb"}``."""
    return [{k: g[k] for k in ("name", "vram_total_mb", "vram_free_mb") if k in g} for g in gpus]


def max_vram_mb(gpus: list[dict[str, Any]]) -> int:
    """Largest single-GPU VRAM in MB (0 without a GPU)."""
    return max((int(g.get("vram_total_mb") or 0) for g in gpus), default=0)
