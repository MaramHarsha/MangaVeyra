"""Local diffusion with Hugging Face diffusers (``pip install 'mangaveyra-worker[diffusers]'``).

Serves ``image.generate``, ``image.edit``, ``image.inpaint``, ``sketch.ink``, ``character.sheet`` and
``character.train`` for every model whose manifest has a ``worker.diffusers`` section (see
:mod:`mangaveyra_worker.diffusion.spec` for the fields) and fits this machine's VRAM.

* Pipelines come from ``AutoPipelineForText2Image`` / ``…Image2Image`` / ``…Inpainting``; derived pipelines are
  built with ``from_pipe`` so they share the base weights.
* A pose (OpenPose JSON or ``pose_image``) switches to the model's OpenPose ControlNet; ``ref_N`` images steer
  identity through an IP-Adapter when the manifest configures one; LoRAs are loaded per job and unloaded after.
* Progress is reported every denoising step (``callback_on_step_end``) with cheap latent previews; cancel takes
  effect at the next step.
* The :class:`~mangaveyra_worker.diffusion.cache.PipelineCache` keeps pipelines resident within a VRAM budget and
  CPU offload is chosen automatically for models larger than the GPU (``MV_OFFLOAD``).
"""

from __future__ import annotations

import inspect
import logging
import time
from typing import Any, Callable, ContextManager

from PIL import Image, ImageFilter, ImageOps

from mangaveyra_worker.backends.base import Backend
from mangaveyra_worker.diffusion.cache import Entry, PipelineCache, free_accelerator
from mangaveyra_worker.diffusion.loras import LoraRef, resolve_loras
from mangaveyra_worker.diffusion.spec import ModelSpec, fits
from mangaveyra_worker.errors import BadInput, JobFailure
from mangaveyra_worker.gpu import max_vram_mb
from mangaveyra_worker.imaging import reference_guide, render_openpose
from mangaveyra_worker.jobs import (
    Job,
    JobContext,
    JobResult,
    clamp_float,
    clamp_int,
    resolve_seed,
    validate_trigger,
)
from mangaveyra_worker.manifests import ModelCatalog

log = logging.getLogger(__name__)

SHEET_SUFFIX = (
    "character reference sheet, model sheet, turnaround, front view, side view, face close-up, "
    "full body, plain white background"
)
DEFAULT_NEGATIVE = "lowres, bad anatomy, bad hands, extra digits, watermark, signature, text, jpeg artifacts"
#: Estimated extra VRAM of a ControlNet when the manifest does not say.
CONTROLNET_MB = 2500

_SCHEDULERS = {
    "euler_a": ("EulerAncestralDiscreteScheduler", {}),
    "euler": ("EulerDiscreteScheduler", {}),
    "dpmpp_2m": ("DPMSolverMultistepScheduler", {}),
    "dpmpp_2m_karras": ("DPMSolverMultistepScheduler", {"use_karras_sigmas": True}),
    "ddim": ("DDIMScheduler", {}),
}


class DiffusersBackend(Backend):
    """diffusers pipelines on the local GPU (or CPU/MPS when configured)."""

    name = "diffusers"

    @classmethod
    def available(cls, config: Any) -> tuple[bool, str]:
        """Needs torch + diffusers importable."""
        try:
            import diffusers  # noqa: F401
            import torch  # noqa: F401
        except ImportError as exc:
            return False, f"{exc} (pip install 'mangaveyra-worker[diffusers]')"
        return True, ""

    def __init__(self, config: Any, gpus: list[dict[str, Any]]) -> None:
        super().__init__(config, gpus)
        import torch
        from diffusers.utils import logging as diffusers_logging

        diffusers_logging.disable_progress_bar()  # progress goes to the server, not to stderr
        self.torch = torch
        self.device = _resolve_device(config.device, torch)
        if self.device.startswith("cuda") and not gpus:
            self.gpus = gpus = [
                {
                    "name": torch.cuda.get_device_name(0),
                    "vram_total_mb": torch.cuda.get_device_properties(0).total_memory // 2**20,
                }
            ]
        vram = max_vram_mb(self.gpus)
        self.budget_mb = config.vram_budget_mb or (int(vram * 0.9) if vram else 10**9)
        self.catalog = ModelCatalog.load(config.models_dir)
        self.specs: dict[str, ModelSpec] = {}
        for manifest in self.catalog.with_section("diffusers"):
            mid = str(manifest["id"])
            if config.models and mid not in config.models:
                continue
            try:
                spec = ModelSpec.from_manifest(mid, manifest["worker"]["diffusers"], manifest)
            except ValueError as exc:
                log.warning("invalid diffusers manifest", extra={"fields": {"model": mid, "error": str(exc)}})
                continue
            if self.device == "cpu" and config.device != "cpu":
                continue  # no accelerator: only an explicit MV_DEVICE=cpu runs diffusion on the CPU
            if fits(spec, vram, config.offload, self.device.split(":")[0]):
                self.specs[mid] = spec
            else:
                log.info(
                    "model does not fit this GPU",
                    extra={
                        "fields": {
                            "model": mid,
                            "vram_mb": spec.vram_mb,
                            "min_vram_mb": spec.min_vram_mb,
                            "gpu_mb": vram,
                        }
                    },
                )
        self.cache = PipelineCache(self.budget_mb)
        if self.device == "cpu" and config.device != "cpu":
            log.info(
                "no CUDA/MPS device: diffusers serves nothing (set MV_DEVICE=cpu to force slow CPU runs)"
            )

    # ------------------------------------------------------------------ capabilities

    def _can_train(self) -> bool:
        if self.config.train_command:
            return True
        try:
            import peft  # noqa: F401
        except ImportError:
            return False
        # The built-in trainer needs CUDA for sensible speed; an explicit MV_DEVICE=cpu allows it (tests, tiny models).
        return self.device.startswith("cuda") or self.config.device == "cpu"

    def job_types(self) -> list[str]:
        """Union of the served types of every advertised model."""
        seen: dict[str, None] = {}
        for spec in self.specs.values():
            for t in spec.served_types(can_train=self._can_train()):
                seen[t] = None
        return list(seen)

    def models(self) -> list[str]:
        """Advertised model ids."""
        return sorted(self.specs)

    def _model_for(self, job: Job) -> str:
        """The job's model id, or — for a job without one — the first advertised model serving its type."""
        if job.model_id is not None:
            return job.model_id
        for mid in sorted(self.specs):
            if job.type in self.specs[mid].served_types(can_train=self._can_train()):
                return mid
        return ""

    def accepts(self, job: Job) -> bool:
        """The job's model must be one of ours and serve the job type."""
        spec = self.specs.get(self._model_for(job))
        return spec is not None and job.type in spec.served_types(can_train=self._can_train())

    def describe(self) -> dict[str, Any]:
        """Startup summary."""
        d = super().describe()
        d.update(device=self.device, vram_budget_mb=self.budget_mb, offload=self.config.offload)
        return d

    def _spec(self, job: Job) -> ModelSpec:
        mid = self._model_for(job)
        local = self.specs.get(mid)
        if local is None:
            raise BadInput(f"model {mid!r} is not served by this worker")
        section = self.catalog.resolve(mid, "diffusers", job.model.manifest)
        try:
            return ModelSpec.from_manifest(mid, section, self.catalog.get(mid) or job.model.manifest)
        except ValueError:
            return local

    # ------------------------------------------------------------------ dispatch

    def run(self, job: Job, ctx: JobContext) -> JobResult:
        """Run one job."""
        spec = self._spec(job)
        handlers: dict[str, Callable[[Job, JobContext, ModelSpec], tuple[dict[str, Any], float]]] = {
            "image.generate": self._generate,
            "image.edit": self._edit,
            "image.inpaint": self._inpaint,
            "sketch.ink": self._sketch,
            "character.sheet": self._sheet,
            "character.train": self._train,
        }
        handler = handlers.get(job.type)
        if handler is None:
            raise BadInput(f"diffusers backend cannot run {job.type}")
        output, gpu = handler(job, ctx, spec)
        return JobResult(output, gpu_seconds=gpu if self.device.startswith("cuda") else 0.0)

    # ------------------------------------------------------------------ pipelines

    def _dtype(self, spec: ModelSpec) -> Any:
        t = self.torch
        if spec.dtype != "auto":
            return getattr(t, spec.dtype)
        if self.device == "cpu":
            return t.float32
        if self.device == "mps":
            return t.float16
        if spec.family in ("sdxl", "sd15"):
            return t.float16
        return t.bfloat16 if t.cuda.is_bf16_supported() else t.float16

    def _offload_mode(self, spec: ModelSpec) -> str:
        if not self.device.startswith("cuda"):
            return "none"
        mode = self.config.offload
        if mode != "auto":
            return mode
        if spec.vram_mb and spec.vram_mb > self.budget_mb:
            if spec.offload_mode == "sequential" or (spec.min_vram_mb or spec.vram_mb) > self.budget_mb:
                return "sequential"
            return "model"
        return "none"

    def _place(self, pipe: Any, spec: ModelSpec) -> Any:
        mode = self._offload_mode(spec)
        gpu_id = int(self.device.split(":")[1]) if ":" in self.device else 0
        if mode == "model":
            pipe.enable_model_cpu_offload(gpu_id=gpu_id)
        elif mode == "sequential":
            pipe.enable_sequential_cpu_offload(gpu_id=gpu_id)
        else:
            pipe.to(self.device)
        if hasattr(pipe, "enable_vae_tiling") and max(spec.default_size) >= 1024:
            try:
                pipe.enable_vae_tiling()  # large pages decode in tiles instead of spiking VRAM
            except Exception:  # some VAEs do not support tiling
                pass
        pipe.set_progress_bar_config(disable=True)
        return pipe

    def _estimate(self, spec: ModelSpec) -> int:
        if self._offload_mode(spec) != "none":
            return spec.min_vram_mb or 4000
        return spec.vram_mb or 8000

    def _load_kwargs(self, spec: ModelSpec) -> dict[str, Any]:
        kw: dict[str, Any] = {"torch_dtype": self._dtype(spec)}
        if spec.variant:
            kw["variant"] = spec.variant
        if spec.revision:
            kw["revision"] = spec.revision
        return kw

    def _base(self, spec: ModelSpec) -> ContextManager[Entry]:
        """Check out the base pipeline of ``spec`` (pinned and locked for the ``with`` body)."""

        def load() -> Any:
            import diffusers

            kw = self._load_kwargs(spec)
            if spec.single_file:
                cls_name = {
                    "sdxl": "StableDiffusionXLPipeline",
                    "sd15": "StableDiffusionPipeline",
                    "sd3": "StableDiffusion3Pipeline",
                    "flux": "FluxPipeline",
                }.get(spec.family)
                if cls_name is None:
                    raise JobFailure(
                        f"single_file is not supported for family {spec.family}", retryable=False
                    )
                if spec.repo:
                    kw["config"] = spec.repo
                kw.pop("variant", None)
                pipe = getattr(diffusers, cls_name).from_single_file(spec.single_file, **kw)
            else:
                try:
                    pipe = diffusers.AutoPipelineForText2Image.from_pretrained(spec.repo, **kw)
                except ValueError:  # pipelines AutoPipeline does not map (e.g. some edit-first models)
                    pipe = diffusers.DiffusionPipeline.from_pretrained(spec.repo, **kw)
            self._set_scheduler(pipe, spec)
            return self._place(pipe, spec)

        return self.cache.checkout(f"{spec.model_id}:base", self._estimate(spec), load)

    def _set_scheduler(self, pipe: Any, spec: ModelSpec) -> None:
        if not spec.scheduler:
            return
        import diffusers

        if spec.scheduler not in _SCHEDULERS:
            raise JobFailure(f"unknown scheduler {spec.scheduler!r}", retryable=False)
        name, extra = _SCHEDULERS[spec.scheduler]
        pipe.scheduler = getattr(diffusers, name).from_config(pipe.scheduler.config, **extra)

    def _derived(self, entry: Entry, spec: ModelSpec, kind: str, controlnet: str | None = None) -> Any:
        """img2img / inpaint / ControlNet variants of the base pipeline (shared weights)."""
        key = f"{kind}:{controlnet or ''}"
        if key in entry.derived:
            return entry.derived[key]
        import diffusers

        auto = {
            "text2img": diffusers.AutoPipelineForText2Image,
            "img2img": diffusers.AutoPipelineForImage2Image,
            "inpaint": diffusers.AutoPipelineForInpainting,
        }[kind]
        extra: dict[str, Any] = {}
        if controlnet:
            cn_cls = diffusers.FluxControlNetModel if spec.family == "flux" else diffusers.ControlNetModel
            extra["controlnet"] = cn_cls.from_pretrained(controlnet, torch_dtype=self._dtype(spec))
            entry.vram_mb += CONTROLNET_MB
        pipe = auto.from_pipe(entry.pipe, **extra)
        mode = self._offload_mode(spec)
        if mode == "model":
            pipe.enable_model_cpu_offload()
        elif mode == "sequential":
            pipe.enable_sequential_cpu_offload()
        elif controlnet:
            pipe.to(self.device)
        pipe.set_progress_bar_config(disable=True)
        entry.derived[key] = pipe
        return pipe

    def _separate(self, spec: ModelSpec, kind: str, repo: str) -> ContextManager[Entry]:
        """Check out a pipeline loaded from its own repo (instruction edit, dedicated inpainting model)."""

        def load() -> Any:
            import diffusers

            kw = self._load_kwargs(spec)
            if kind == "inpaint":
                pipe = diffusers.AutoPipelineForInpainting.from_pretrained(repo, **kw)
            else:
                pipe = diffusers.DiffusionPipeline.from_pretrained(repo, **kw)
            return self._place(pipe, spec)

        return self.cache.checkout(f"{spec.model_id}:{kind}:{repo}", self._estimate(spec), load)

    # ------------------------------------------------------------------ inference helpers

    def _call(
        self,
        pipe: Any,
        spec: ModelSpec,
        ctx: JobContext,
        kwargs: dict[str, Any],
        *,
        offset: int,
        total: int,
        span: tuple[float, float] = (0.0, 1.0),
    ) -> Image.Image:
        """Call a pipeline with only the named kwargs it accepts, reporting every step.

        Step ``offset + i`` of ``total`` maps onto the ``span`` of the job's progress bar.
        """
        from mangaveyra_worker.diffusion.preview import latent_preview

        params = inspect.signature(pipe.__call__).parameters
        call = {k: v for k, v in kwargs.items() if v is not None and k in params}
        lo, hi = span

        def on_step(_pipe: Any, i: int, _t: Any, cb: dict[str, Any]) -> dict[str, Any]:
            done = min(total, offset + i + 1)
            preview = None
            if (i + 1) % 5 == 0 and "latents" in cb:
                try:
                    preview = latent_preview(cb["latents"], spec.family)
                except Exception:  # previews are best effort
                    preview = None
            ctx.progress(
                min(0.99, lo + (hi - lo) * done / total),
                step=done,
                steps=total,
                message="denoising",
                preview=preview,
            )
            return cb

        if "callback_on_step_end" in params:
            call["callback_on_step_end"] = on_step
        ctx.check()
        with self.torch.inference_mode():
            result = pipe(**call)
        return result.images[0]

    def _generator(self, seed: int) -> Any:
        return self.torch.Generator(device="cpu").manual_seed(seed)

    def _loras(self, job: Job, ctx: JobContext) -> list[LoraRef]:
        return resolve_loras(
            job.input.get("loras"),
            job.files,
            cache_dir=self.config.data_dir / "loras",
            lora_dir=self.config.lora_dir,
            download=ctx.client.download,
        )

    def _with_adapters(
        self,
        pipe: Any,
        spec: ModelSpec,
        loras: list[LoraRef],
        refs: list[Image.Image],
        fn: Callable[[dict[str, Any]], Any],
    ) -> Any:
        """Load LoRAs and the IP-Adapter for one job, run ``fn(extra_kwargs)``, then unload them."""
        extra: dict[str, Any] = {}
        loaded_lora = loaded_ip = False
        try:
            if loras:
                for ref in loras:
                    pipe.load_lora_weights(
                        str(ref.path.parent), weight_name=ref.path.name, adapter_name=ref.adapter
                    )
                loaded_lora = True
                pipe.set_adapters([r.adapter for r in loras], adapter_weights=[r.weight for r in loras])
            if refs and spec.ip_adapter:
                ip = spec.ip_adapter
                pipe.load_ip_adapter(
                    ip["repo"],
                    subfolder=ip.get("subfolder", "sdxl_models"),
                    weight_name=ip.get("weight_name", "ip-adapter_sdxl.bin"),
                )
                loaded_ip = True
                pipe.set_ip_adapter_scale(float(ip.get("scale", 0.6)))
                extra["ip_adapter_image"] = reference_guide(refs)
            return fn(extra)
        finally:
            if loaded_lora:
                try:
                    pipe.unload_lora_weights()
                except Exception as exc:  # pragma: no cover - keep the pipeline usable
                    log.warning("LoRA unload failed", extra={"fields": {"error": str(exc)}})
            if loaded_ip:
                try:
                    pipe.unload_ip_adapter()
                except Exception as exc:  # pragma: no cover
                    log.warning("IP-Adapter unload failed", extra={"fields": {"error": str(exc)}})

    @staticmethod
    def _finish(img: Image.Image, style: str | None) -> Image.Image:
        if style in ("bw_screentone", "screentone", "flat_grey"):
            return ImageOps.grayscale(img).convert("RGB")
        return img

    def _variations(
        self,
        ctx: JobContext,
        n: int,
        seed: int,
        steps: int,
        render: Callable[[int, int, int, int], Image.Image],
        meta: dict[str, Any],
    ) -> tuple[dict[str, Any], float]:
        images = []
        started = time.monotonic()
        for i in range(n):
            s = (seed + i) % 2**32
            img = render(i, s, i * steps, n * steps)
            key = ctx.upload_image(
                f"variation-{i + 1}",
                img,
                meta={**meta, "seed": s, "variation": i + 1, "width": img.width, "height": img.height},
            )
            images.append({"key": key, "seed": s})
        return {"images": images}, time.monotonic() - started

    # ------------------------------------------------------------------ job types

    def _generate(
        self, job: Job, ctx: JobContext, spec: ModelSpec, *, sheet: bool = False
    ) -> tuple[dict[str, Any], float]:
        inp = job.input
        n = clamp_int(inp.get("n"), 1, 1, 4, "n")
        steps = clamp_int(inp.get("steps"), spec.default_steps, 1, 150, "steps")
        guidance = clamp_float(inp.get("guidance"), spec.default_guidance, 0.0, 30.0, "guidance")
        seed = resolve_seed(inp.get("seed"))
        style = inp.get("style")
        if sheet:
            # A 3:2 landscape sheet with the model's native pixel budget (SDXL: 1248×832).
            area = spec.default_size[0] * spec.default_size[1]
            base_w, base_h = spec.size(round((area * 1.5) ** 0.5), round((area / 1.5) ** 0.5))
        else:
            try:
                base_w, base_h = spec.size(inp.get("width"), inp.get("height"))
            except ValueError as exc:
                raise BadInput(str(exc)) from exc
        prompt = spec.prompt(str(inp.get("prompt") or ""), style)
        if sheet:
            prompt = f"{prompt}, {SHEET_SUFFIX}"
        negative = spec.negative(inp.get("negative") or DEFAULT_NEGATIVE)
        pose_img = None
        if not sheet and ctx.has_file("pose_image"):
            pose_img = ctx.image("pose_image").resize((base_w, base_h))
        elif not sheet and isinstance(inp.get("pose"), dict):
            pose_img = render_openpose(inp["pose"], base_w, base_h)
        if pose_img is not None and not spec.controlnet_openpose:
            raise BadInput("This model has no configured OpenPose ControlNet")
        refs = ctx.images("ref_")
        loras = self._loras(job, ctx)
        with self._base(spec) as entry:
            if pose_img is not None and spec.controlnet_openpose:
                pipe = self._derived(entry, spec, "text2img", spec.controlnet_openpose)
            else:
                pipe = entry.pipe

            def render(i: int, s: int, offset: int, total: int) -> Image.Image:
                def run(extra: dict[str, Any]) -> Image.Image:
                    kw = dict(
                        prompt=prompt,
                        negative_prompt=negative,
                        num_inference_steps=steps,
                        guidance_scale=guidance,
                        width=base_w,
                        height=base_h,
                        generator=self._generator(s),
                        **extra,
                    )
                    if pose_img is not None:
                        kw["image"] = pose_img
                        kw["controlnet_conditioning_scale"] = 0.8
                    return self._call(pipe, spec, ctx, kw, offset=offset, total=total)

                return self._finish(self._with_adapters(pipe, spec, loras, refs, run), style)

            return self._variations(ctx, n, seed, steps, render, {"model": spec.model_id})

    def _sheet(self, job: Job, ctx: JobContext, spec: ModelSpec) -> tuple[dict[str, Any], float]:
        return self._generate(job, ctx, spec, sheet=True)

    def _edit(self, job: Job, ctx: JobContext, spec: ModelSpec) -> tuple[dict[str, Any], float]:
        inp = job.input
        n = clamp_int(inp.get("n"), 1, 1, 4, "n")
        strength = clamp_float(inp.get("strength"), 0.6, 0.05, 1.0, "strength")
        steps = clamp_int(inp.get("steps"), spec.default_steps, 1, 150, "steps")
        seed = resolve_seed(inp.get("seed"))
        source = ctx.image("image")
        w, h = spec.size(source.width, source.height)
        init = source.resize((w, h), Image.Resampling.LANCZOS)
        prompt = spec.prompt(str(inp.get("prompt") or ""), inp.get("style"))
        negative = spec.negative(inp.get("negative") or DEFAULT_NEGATIVE)
        refs = ctx.images("ref_")
        loras = self._loras(job, ctx)
        if spec.edit == "instruct":
            checkout = self._separate(spec, "instruct", spec.edit_repo or spec.repo or "")
            effective = steps
        else:
            checkout = self._base(spec)
            effective = max(1, int(steps * strength))  # img2img runs only the last `strength` of the schedule
        with checkout as entry:
            pipe = entry.pipe if spec.edit == "instruct" else self._derived(entry, spec, "img2img")

            def render(i: int, s: int, offset: int, total: int) -> Image.Image:
                def run(extra: dict[str, Any]) -> Image.Image:
                    kw = dict(
                        prompt=prompt,
                        negative_prompt=negative,
                        image=init,
                        num_inference_steps=steps,
                        guidance_scale=spec.default_guidance,
                        true_cfg_scale=spec.default_guidance,
                        generator=self._generator(s),
                        **extra,
                    )
                    if spec.edit != "instruct":
                        kw["strength"] = strength
                    return self._call(pipe, spec, ctx, kw, offset=offset, total=total)

                out = self._with_adapters(pipe, spec, loras, refs if spec.edit != "instruct" else [], run)
                return self._finish(out.resize(source.size, Image.Resampling.LANCZOS), inp.get("style"))

            return self._variations(
                ctx, n, seed, effective, render, {"model": spec.model_id, "strength": strength}
            )

    def _inpaint(self, job: Job, ctx: JobContext, spec: ModelSpec) -> tuple[dict[str, Any], float]:
        inp = job.input
        n = clamp_int(inp.get("n"), 1, 1, 4, "n")
        feather = clamp_int(inp.get("feather_px"), 8, 0, 256, "feather_px")
        steps = clamp_int(inp.get("steps"), spec.default_steps, 1, 150, "steps")
        strength = clamp_float(inp.get("strength"), 0.99, 0.05, 1.0, "strength")
        seed = resolve_seed(inp.get("seed"))
        source = ctx.image("image")
        mask = ctx.image("mask", mode="L")
        if mask.size != source.size:
            mask = mask.resize(source.size, Image.Resampling.NEAREST)
        if mask.getbbox() is None:
            raise BadInput("mask is empty (white = change)")
        w, h = spec.size(source.width, source.height)
        init = source.resize((w, h), Image.Resampling.LANCZOS)
        soft = mask.filter(ImageFilter.GaussianBlur(feather / 2)) if feather else mask
        mask_in = soft.resize((w, h), Image.Resampling.BILINEAR)
        prompt = spec.prompt(str(inp.get("prompt") or ""), inp.get("style"))
        negative = spec.negative(inp.get("negative") or DEFAULT_NEGATIVE)
        loras = self._loras(job, ctx)
        refs = ctx.images("ref_")
        checkout = (
            self._separate(spec, "inpaint", spec.inpaint_repo) if spec.inpaint_repo else self._base(spec)
        )
        with checkout as entry:
            pipe = entry.pipe if spec.inpaint_repo else self._derived(entry, spec, "inpaint")

            def render(i: int, s: int, offset: int, total: int) -> Image.Image:
                def run(extra: dict[str, Any]) -> Image.Image:
                    kw = dict(
                        prompt=prompt,
                        negative_prompt=negative,
                        image=init,
                        mask_image=mask_in,
                        strength=strength,
                        num_inference_steps=steps,
                        guidance_scale=spec.default_guidance,
                        width=w,
                        height=h,
                        generator=self._generator(s),
                        **extra,
                    )
                    return self._call(pipe, spec, ctx, kw, offset=offset, total=total)

                out = self._with_adapters(pipe, spec, loras, refs, run).resize(
                    source.size, Image.Resampling.LANCZOS
                )
                # The server copies outside pixels back exactly; blending the feathered edge here avoids seams.
                return Image.composite(out, source, soft)

            return self._variations(
                ctx, n, seed, max(1, int(steps * strength)), render, {"model": spec.model_id}
            )

    def _sketch(self, job: Job, ctx: JobContext, spec: ModelSpec) -> tuple[dict[str, Any], float]:
        inp = job.input
        exactness = clamp_float(inp.get("strength"), 0.7, 0.0, 1.0, "strength")
        finish = str(inp.get("finish") or "screentone")
        style = {"screentone": "bw_screentone", "flat_grey": "flat_grey", "colour": "colour"}.get(
            finish, "bw_screentone"
        )
        n = clamp_int(inp.get("n"), 1, 1, 4, "n")
        steps = clamp_int(inp.get("steps"), spec.default_steps, 1, 150, "steps")
        seed = resolve_seed(inp.get("seed"))
        sketch = ctx.image("sketch")
        w, h = spec.size(sketch.width, sketch.height)
        base_prompt = str(inp.get("prompt") or "") or "clean manga illustration"
        prompt = spec.prompt(f"{base_prompt}, clean ink lineart", style)
        negative = spec.negative(inp.get("negative") or DEFAULT_NEGATIVE)
        with self._base(spec) as entry:
            if spec.controlnet_lineart:
                pipe = self._derived(entry, spec, "text2img", spec.controlnet_lineart)
                # Lineart ControlNets expect white strokes on black.
                control = ImageOps.invert(ImageOps.grayscale(sketch)).convert("RGB").resize((w, h))
                effective = steps
            else:
                pipe = self._derived(entry, spec, "img2img")
                control = sketch.convert("RGB").resize((w, h), Image.Resampling.LANCZOS)
                denoise = 0.9 - 0.55 * exactness  # exact (1.0) keeps the drawing, loose (0.0) redraws it
                effective = max(1, int(steps * denoise))

            def render(i: int, s: int, offset: int, total: int) -> Image.Image:
                kw: dict[str, Any] = dict(
                    prompt=prompt,
                    negative_prompt=negative,
                    num_inference_steps=steps,
                    guidance_scale=spec.default_guidance,
                    generator=self._generator(s),
                )
                if spec.controlnet_lineart:
                    kw.update(
                        image=control, width=w, height=h, controlnet_conditioning_scale=0.5 + 0.5 * exactness
                    )
                else:
                    kw.update(image=control, strength=0.9 - 0.55 * exactness)
                out = self._call(pipe, spec, ctx, kw, offset=offset, total=total)
                return self._finish(out.resize(sketch.size, Image.Resampling.LANCZOS), style)

            return self._variations(
                ctx, n, seed, effective, render, {"model": spec.model_id, "finish": finish}
            )

    def _train(self, job: Job, ctx: JobContext, spec: ModelSpec) -> tuple[dict[str, Any], float]:
        from mangaveyra_worker.train.base import TrainRequest
        from mangaveyra_worker.vision.embed import score_identity

        inp = job.input
        trigger = validate_trigger(inp.get("trigger"))
        steps = clamp_int(inp.get("steps"), 1000, 1, 20000, "steps")
        rank = clamp_int(inp.get("rank"), 16, 1, 256, "rank")
        base_id = str(inp.get("base_model") or spec.model_id)
        base = self.specs.get(base_id, spec)
        if not base.trainable and not self.config.train_command:
            raise BadInput(f"model {base_id!r} is not trainable on this worker")
        refs = ctx.images("ref_")
        if len(refs) < 3:
            raise BadInput("character.train needs at least 3 reference images (ref_0…)")
        started = time.monotonic()
        train_share = 0.85

        def progress(step: int, total: int, message: str) -> None:
            if step < 0:
                ctx.check()
                return
            ctx.progress(
                train_share * step / max(1, total), step=step, steps=total, message=message or "training"
            )

        req = TrainRequest(
            trigger=trigger,
            images=refs,
            steps=steps,
            rank=rank,
            base_model=base_id,
            base_path=base.single_file or base.repo or "",
            work_dir=ctx.scratch,
            resolution=base.train_resolution,
            seed=resolve_seed(inp.get("seed")),
            learning_rate=clamp_float(inp.get("learning_rate"), 1e-4, 1e-6, 1e-2, "learning_rate"),
            progress=progress,
        )
        if self.config.train_command:
            from mangaveyra_worker.train.external import ExternalTrainer

            trainer: Any = ExternalTrainer(self.config.train_command)
        else:
            from mangaveyra_worker.train.lora_sdxl import PeftSdxlTrainer

            if base.family != "sdxl":
                raise BadInput(
                    "the built-in trainer supports SDXL-family models; set MV_TRAIN_COMMAND for others"
                )
            self.cache.clear()  # training needs the whole GPU
            trainer = PeftSdxlTrainer(
                device=self.device, dtype=self._dtype(base), single_file=bool(base.single_file)
            )
        path = trainer.train(req)
        free_accelerator()
        lora_key = ctx.upload(
            "lora",
            path.read_bytes(),
            content_type="application/octet-stream",
            filename=f"{trigger}.safetensors",
            meta={
                "trigger": trigger,
                "base_model": base_id,
                "rank": rank,
                "steps": steps,
                "trainer": trainer.name,
            },
        )
        # Test renders with the fresh LoRA, then score them against the references.
        tests: list[dict[str, Any]] = []
        test_images: list[Image.Image] = []
        lora = LoraRef(key=lora_key, path=path, weight=0.9, adapter="mv_train")
        with self._base(base) as entry:
            for i, (s, pose) in enumerate(
                ((req.seed + 1, "portrait, looking at viewer"), (req.seed + 2, "full body, standing"))
            ):
                size = base.size(*base.default_size)

                def run(
                    extra: dict[str, Any],
                    s: int = s,
                    pose: str = pose,
                    i: int = i,
                    w: int = size[0],
                    h: int = size[1],
                ) -> Image.Image:
                    kw = dict(
                        prompt=base.prompt(f"{trigger}, {pose}", "bw_screentone"),
                        negative_prompt=base.negative(DEFAULT_NEGATIVE),
                        num_inference_steps=base.default_steps,
                        guidance_scale=base.default_guidance,
                        width=w,
                        height=h,
                        generator=self._generator(s),
                        **extra,
                    )
                    lo = train_share + 0.06 * i
                    return self._call(
                        entry.pipe, base, ctx, kw, offset=0, total=base.default_steps, span=(lo, lo + 0.06)
                    )

                img = self._with_adapters(entry.pipe, base, [lora], [], run)
                test_images.append(img)
                tests.append({"key": ctx.upload_image(f"test-{i + 1}", img, meta={"seed": s}), "seed": s})
        ctx.progress(0.97, message="scoring", force=True)
        try:
            model_dir = self.config.ccip_model_dir if self.config.embedder == "ccip" else self.config.dino_model_dir
            scores, method = score_identity(refs, test_images, self.config.embedder, model_dir)
            score = round(sum(scores) / len(scores), 3)
        except Exception as exc:  # scoring is advisory; the LoRA is still useful
            ctx.log.warning("could not score test images", extra={"fields": {"error": str(exc)}})
            score, method = None, "none"
        return (
            {"lora": {"key": lora_key}, "test_images": tests, "score": score, "score_method": method},
            time.monotonic() - started,
        )

    def close(self) -> None:
        """Free every pipeline."""
        self.cache.clear()


def _resolve_device(setting: str, torch: Any) -> str:
    if setting != "auto":
        return setting
    if torch.cuda.is_available():
        return "cuda"
    if getattr(torch.backends, "mps", None) and torch.backends.mps.is_available():
        return "mps"
    return "cpu"
