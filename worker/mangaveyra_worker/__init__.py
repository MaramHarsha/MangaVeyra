"""MangaVeyra AI worker.

A worker is a separate process that *pulls* jobs (image generation, edits, inpainting, character sheets,
LoRA training, identity scoring, panel detection, OCR) from a MangaVeyra server over the worker protocol v1
(``sdk/api/worker.md``) and streams progress back. The server never connects to a worker.

Backends:

* ``mock`` — always available, needs only Pillow; deterministic labelled screentone placeholders so every
  product flow works on a machine without a GPU.
* ``diffusers`` — local PyTorch/diffusers pipelines (``pip install 'mangaveyra-worker[diffusers]'``).
* ``comfyui`` — runs user-exported ComfyUI workflows through the ComfyUI HTTP API.
* ``vision`` — OpenCV panel/bubble detection, identity scoring and pluggable OCR.
"""

__version__ = "1.0.0"

PROTOCOL_VERSION = "v1"

__all__ = ["__version__", "PROTOCOL_VERSION"]
