"""Model specs for the diffusers backend (``worker.diffusers`` manifest section). No torch import here.

Fields (all optional except ``repo`` or ``single_file``):

==========================  =======================================================================================
``repo``                    Hugging Face repo id or local diffusers folder (``from_pretrained``).
``single_file``             A ``.safetensors`` checkpoint path or URL (``from_single_file``) — common for community
                            SDXL/Illustrious fine-tunes. ``repo`` then names the base config (e.g. SDXL base).
``family``                  ``sdxl`` (default), ``sd15``, ``sd3``, ``flux``, ``qwen-image`` or ``auto``. Picks
                            default sizes, latent previews and whether LoRA training is supported.
``dtype``                   ``auto``, ``float16``, ``bfloat16`` or ``float32``.
``variant`` / ``revision``  Passed to ``from_pretrained`` (e.g. ``variant: fp16``).
``edit``                    ``img2img`` (default: ``AutoPipelineForImage2Image`` sharing the base weights) or
                            ``instruct`` (a separate instruction-editing pipeline such as Qwen-Image-Edit or
                            FLUX Kontext, loaded from ``edit_repo`` with ``DiffusionPipeline``).
``edit_repo``               Repo for ``edit: instruct``.
``inpaint_repo``            Dedicated inpainting checkpoint (default: ``AutoPipelineForInpainting`` from the base).
``controlnet_openpose``     ControlNet repo used when a job carries a pose (``pose`` JSON or ``pose_image``).
``controlnet_lineart``      ControlNet repo used by ``sketch.ink`` (falls back to img2img without it).
``ip_adapter``              ``{repo, subfolder, weight_name, scale}`` — reference images (``ref_N``) steer identity.
``default_steps``           Default denoising steps (28).
``default_guidance``        Default CFG / guidance (6.0).
``default_size``            ``[width, height]`` when the job gives none (family default).
``max_side``                Largest generated side (family default); larger requests are scaled down.
``multiple_of``             Output sides are rounded to this multiple (8, or 16 for flux/qwen).
``prompt_suffix``           Appended to every prompt (e.g. quality tags for Illustrious).
``negative_prefix``         Prepended to every negative prompt.
``scheduler``               ``euler_a``, ``euler``, ``dpmpp_2m``, ``dpmpp_2m_karras``, ``ddim`` or empty (model default).
``trainable``               Allow ``character.train`` on this model (sdxl/sd15 families; built-in trainer is SDXL).
``train_resolution``        Training resolution (1024; use 768 on 12 GB cards).
``offload_mode``            ``model`` (default) or ``sequential``: which CPU offload to use when the model does not
                            fit the GPU. Very large single components (a 20B transformer) need ``sequential``.
``job_types``               Restrict the job types served with this model.
==========================  =======================================================================================

Manifest top-level ``vram_mb`` (fully on GPU) and ``min_vram_mb`` (with CPU offload) drive advertising and the
VRAM-aware cache.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Mapping

FAMILY_DEFAULTS: dict[str, dict[str, Any]] = {
    "sd15": {"size": (512, 768), "max_side": 1024, "multiple_of": 8, "trainable": True},
    "sdxl": {"size": (896, 1152), "max_side": 2048, "multiple_of": 8, "trainable": True},
    "sd3": {"size": (1024, 1024), "max_side": 2048, "multiple_of": 16, "trainable": False},
    "flux": {"size": (1024, 1024), "max_side": 2048, "multiple_of": 16, "trainable": False},
    "qwen-image": {"size": (1024, 1024), "max_side": 2048, "multiple_of": 16, "trainable": False},
    "auto": {"size": (1024, 1024), "max_side": 2048, "multiple_of": 16, "trainable": False},
}

#: Job types a diffusers model can serve (``character.train`` only when trainable).
DIFFUSERS_TYPES = (
    "image.generate",
    "image.edit",
    "image.inpaint",
    "sketch.ink",
    "character.sheet",
    "character.train",
)

#: Prompt additions per finishing style (the server may already fold style into the prompt; these are light).
STYLE_SUFFIX = {
    "bw_screentone": "monochrome, greyscale, manga, ink lineart, screentone",
    "screentone": "monochrome, greyscale, manga, ink lineart, screentone",
    "flat_grey": "greyscale, manga, clean lineart, flat grey tones",
    "colour": "full colour, anime colouring, clean lineart",
}


@dataclass
class ModelSpec:
    """How to run one model with diffusers."""

    model_id: str
    repo: str | None = None
    single_file: str | None = None
    family: str = "sdxl"
    dtype: str = "auto"
    variant: str | None = None
    revision: str | None = None
    edit: str = "img2img"
    edit_repo: str | None = None
    inpaint_repo: str | None = None
    controlnet_openpose: str | None = None
    controlnet_lineart: str | None = None
    ip_adapter: dict[str, Any] | None = None
    default_steps: int = 28
    default_guidance: float = 6.0
    default_size: tuple[int, int] = (1024, 1024)
    max_side: int = 2048
    multiple_of: int = 8
    prompt_suffix: str = ""
    negative_prefix: str = ""
    scheduler: str = ""
    trainable: bool = False
    train_resolution: int = 1024
    job_types: list[str] = field(default_factory=list)
    offload_mode: str = "model"
    vram_mb: int = 0
    min_vram_mb: int = 0

    @classmethod
    def from_manifest(
        cls, model_id: str, section: Mapping[str, Any], manifest: Mapping[str, Any] | None = None
    ) -> "ModelSpec":
        """Build a spec from a ``worker.diffusers`` section (+ the manifest for VRAM numbers)."""
        if not section.get("repo") and not section.get("single_file"):
            raise ValueError(f"{model_id}: worker.diffusers needs repo or single_file")
        family = str(section.get("family") or "sdxl")
        if family not in FAMILY_DEFAULTS:
            raise ValueError(f"{model_id}: unknown family {family!r}")
        fam = FAMILY_DEFAULTS[family]
        size = section.get("default_size") or fam["size"]
        manifest = manifest or {}
        vram = int(manifest.get("vram_mb") or section.get("vram_mb") or 0)
        spec = cls(
            model_id=model_id,
            repo=section.get("repo"),
            single_file=section.get("single_file"),
            family=family,
            dtype=str(section.get("dtype") or "auto"),
            variant=section.get("variant"),
            revision=section.get("revision"),
            edit=str(section.get("edit") or ("instruct" if section.get("edit_repo") else "img2img")),
            edit_repo=section.get("edit_repo"),
            inpaint_repo=section.get("inpaint_repo"),
            controlnet_openpose=section.get("controlnet_openpose"),
            controlnet_lineart=section.get("controlnet_lineart"),
            ip_adapter=dict(section["ip_adapter"])
            if isinstance(section.get("ip_adapter"), Mapping)
            else None,
            default_steps=int(section.get("default_steps") or 28),
            default_guidance=float(
                section.get("default_guidance") if section.get("default_guidance") is not None else 6.0
            ),
            default_size=(int(size[0]), int(size[1])),
            max_side=int(section.get("max_side") or fam["max_side"]),
            multiple_of=int(section.get("multiple_of") or fam["multiple_of"]),
            prompt_suffix=str(section.get("prompt_suffix") or ""),
            negative_prefix=str(section.get("negative_prefix") or ""),
            scheduler=str(section.get("scheduler") or ""),
            trainable=bool(section.get("trainable", fam["trainable"])) and family in ("sdxl", "sd15"),
            train_resolution=int(section.get("train_resolution") or (1024 if family == "sdxl" else 512)),
            job_types=[str(t) for t in section.get("job_types") or []],
            offload_mode=str(section.get("offload_mode") or "model"),
            vram_mb=vram,
            min_vram_mb=int(manifest.get("min_vram_mb") or section.get("min_vram_mb") or vram),
        )
        if spec.offload_mode not in ("model", "sequential"):
            raise ValueError(f"{model_id}: offload_mode must be model or sequential")
        if spec.edit not in ("img2img", "instruct"):
            raise ValueError(f"{model_id}: edit must be img2img or instruct")
        if spec.edit == "instruct" and not spec.edit_repo:
            spec.edit_repo = spec.repo
        return spec

    def served_types(self, *, can_train: bool) -> list[str]:
        """Job types this model serves on this worker."""
        types = list(self.job_types) if self.job_types else list(DIFFUSERS_TYPES)
        if "character.train" in types and not (self.trainable and can_train):
            types.remove("character.train")
        return [t for t in types if t in DIFFUSERS_TYPES]

    def size(self, width: Any, height: Any) -> tuple[int, int]:
        """Snap a requested size to ``multiple_of`` and ``max_side``, keeping the aspect ratio."""
        w = int(width or self.default_size[0])
        h = int(height or self.default_size[1])
        if w <= 0 or h <= 0:
            raise ValueError("width and height must be positive")
        scale = min(1.0, self.max_side / max(w, h))
        m = self.multiple_of
        w = max(m, int(w * scale / m + 0.5) * m)  # round half up (not to even)
        h = max(m, int(h * scale / m + 0.5) * m)
        return w, h

    def prompt(self, prompt: str, style: str | None) -> str:
        """Final prompt: job prompt + style suffix + model suffix."""
        parts = [prompt.strip()]
        if style and STYLE_SUFFIX.get(style):
            parts.append(STYLE_SUFFIX[style])
        if self.prompt_suffix:
            parts.append(self.prompt_suffix)
        return ", ".join(p for p in parts if p)

    def negative(self, negative: str | None) -> str:
        """Final negative prompt."""
        return ", ".join(p for p in (self.negative_prefix, (negative or "").strip()) if p)


def fits(spec: ModelSpec, vram_total_mb: int, offload: str, device: str) -> bool:
    """Whether a model may be advertised on this machine.

    On a GPU: fully resident if ``vram_mb`` fits; with offload (``auto``/``model``/``sequential``) when
    ``min_vram_mb`` fits. Without a GPU only an explicit ``MV_DEVICE=cpu`` (slow, but works) advertises models.
    """
    if device == "cpu":
        return True
    if vram_total_mb <= 0:
        return device in ("mps",)
    need_full = spec.vram_mb or 0
    need_min = spec.min_vram_mb or need_full
    if need_full <= vram_total_mb:
        return True
    return offload != "none" and need_min <= vram_total_mb
