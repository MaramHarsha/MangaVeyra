"""Helpers for the diffusers backend: model specs, the VRAM-aware pipeline cache, LoRA resolution and latent
previews. ``spec``, ``cache`` and ``loras`` import no torch, so they are unit-tested without the heavy extra."""
