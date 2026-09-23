"""Cheap live previews from diffusion latents (no VAE decode).

A 4-channel SD/SDXL latent is projected to RGB with a fixed linear map (the same approximation ComfyUI and
A1111 use for "latent2rgb" previews). Other latent spaces return ``None`` and simply get no preview.
"""

from __future__ import annotations

from typing import Any

from PIL import Image

_FACTORS = {
    "sdxl": (
        [
            [0.3651, 0.4232, 0.4341],
            [-0.2533, -0.0042, 0.1068],
            [0.1076, 0.1111, -0.0362],
            [-0.3165, -0.2492, -0.2188],
        ],
        [0.1084, -0.0175, -0.0011],
    ),
    "sd15": (
        [
            [0.3512, 0.2297, 0.3227],
            [0.3250, 0.4974, 0.2350],
            [-0.2829, 0.1762, 0.2721],
            [-0.2120, -0.2616, -0.7177],
        ],
        [0.0, 0.0, 0.0],
    ),
}


def latent_preview(latents: Any, family: str) -> Image.Image | None:
    """Approximate RGB image of the first latent in a ``[B, 4, H, W]`` torch tensor, or ``None``."""
    if family not in _FACTORS or getattr(latents, "ndim", 0) != 4 or latents.shape[1] != 4:
        return None
    import torch

    weights, bias = _FACTORS[family]
    w = torch.tensor(weights, dtype=torch.float32, device=latents.device)
    b = torch.tensor(bias, dtype=torch.float32, device=latents.device)
    rgb = torch.einsum("chw,cr->hwr", latents[0].float(), w) + b
    rgb = ((rgb + 1.0) * 127.5).clamp(0, 255).to(torch.uint8).cpu().numpy()
    img = Image.fromarray(rgb, "RGB")
    return img.resize((img.width * 4, img.height * 4), Image.Resampling.BILINEAR)
