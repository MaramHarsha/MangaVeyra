"""GPU detection parsing."""

from __future__ import annotations

from mangaveyra_worker.gpu import max_vram_mb, parse_nvidia_smi, protocol_gpus

SMI = """0, NVIDIA GeForce RTX 4090, 24564, 20110
1, NVIDIA RTX A6000, 49140, 48000
2, Some, GPU, With, Commas, 8192, [N/A]
"""


def test_parse_nvidia_smi() -> None:
    gpus = parse_nvidia_smi(SMI)
    assert gpus[0] == {
        "index": 0,
        "name": "NVIDIA GeForce RTX 4090",
        "vram_total_mb": 24564,
        "vram_free_mb": 20110,
    }
    assert gpus[1]["vram_total_mb"] == 49140
    assert len(gpus) == 2  # the [N/A] row is skipped
    assert max_vram_mb(gpus) == 49140


def test_protocol_shape() -> None:
    assert protocol_gpus(parse_nvidia_smi(SMI))[0] == {
        "name": "NVIDIA GeForce RTX 4090",
        "vram_total_mb": 24564,
        "vram_free_mb": 20110,
    }
    assert max_vram_mb([]) == 0
