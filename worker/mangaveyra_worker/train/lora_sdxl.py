"""A compact SDXL / Illustrious / NoobAI character-LoRA trainer on diffusers + peft.

It follows the structure of diffusers' ``train_text_to_image_lora_sdxl.py`` reduced to what Character Lock needs:
UNet attention LoRA (``to_q/to_k/to_v/to_out.0``), frozen text encoders, cached latents and prompt embeddings,
batch size 1, AdamW, gradient checkpointing and mixed precision. With 10–20 reference images, ``rank`` 16 and
800–1500 steps it produces a usable identity LoRA in roughly 15–30 minutes on a 16–24 GB GPU (use
``train_resolution: 768`` in the manifest on 12 GB cards).

Requires the ``diffusers`` extra. Heavy imports happen inside :meth:`PeftSdxlTrainer.train`.
"""

from __future__ import annotations

import logging
import random
from pathlib import Path
from typing import Any

from mangaveyra_worker.train.base import Trainer, TrainRequest, square_crop

log = logging.getLogger(__name__)


class PeftSdxlTrainer(Trainer):
    """Built-in LoRA trainer for SDXL-family checkpoints."""

    name = "peft-sdxl"

    def __init__(self, *, device: str, dtype: Any, single_file: bool = False) -> None:
        self.device = device
        self.dtype = dtype
        self.single_file = single_file

    def _load(self, base_path: str) -> Any:
        from diffusers import StableDiffusionXLPipeline

        if self.single_file:
            return StableDiffusionXLPipeline.from_single_file(base_path, torch_dtype=self.dtype)
        return StableDiffusionXLPipeline.from_pretrained(base_path, torch_dtype=self.dtype)

    def train(self, req: TrainRequest) -> Path:
        """Train and save ``pytorch_lora_weights.safetensors`` under ``req.work_dir/output``."""
        import numpy as np
        import torch
        import torch.nn.functional as F
        from diffusers import DDPMScheduler, StableDiffusionXLPipeline
        from diffusers.training_utils import cast_training_params
        from diffusers.utils import convert_state_dict_to_diffusers
        from peft import LoraConfig
        from peft.utils import get_peft_model_state_dict

        random.seed(req.seed)
        torch.manual_seed(req.seed)
        device = torch.device(self.device)
        use_amp = device.type == "cuda" and self.dtype in (torch.float16, torch.bfloat16)

        req.progress(0, req.steps, "loading base model")
        pipe = self._load(req.base_path)
        unet, vae = pipe.unet, pipe.vae
        te1, te2 = pipe.text_encoder, pipe.text_encoder_2
        tok1, tok2 = pipe.tokenizer, pipe.tokenizer_2
        scheduler = DDPMScheduler.from_config(pipe.scheduler.config)
        for module in (unet, vae, te1, te2):
            module.requires_grad_(False)

        # ---- cache latents and prompt embeddings (the encoders are then moved off the GPU)
        res = req.resolution
        vae.to(device, dtype=torch.float32)  # the SDXL VAE overflows in fp16
        te1.to(device)
        te2.to(device)
        latents_cache: list[torch.Tensor] = []
        embeds_cache: list[tuple[torch.Tensor, torch.Tensor]] = []
        with torch.no_grad():
            for i, img in enumerate(req.images):
                # Asymmetric hair clips, scars and costume details are identity
                # constraints: mirroring them silently teaches the wrong side.
                im = square_crop(img, res)
                arr = torch.from_numpy(np.asarray(im, dtype=np.float32) / 127.5 - 1.0).permute(2, 0, 1)
                lat = vae.encode(arr.unsqueeze(0).to(device)).latent_dist.sample()
                latents_cache.append((lat * vae.config.scaling_factor).to("cpu"))
                embeds_cache.append(self._encode(req.caption(i), tok1, tok2, te1, te2, device))
                req.progress(0, req.steps, f"caching {i + 1}/{len(req.images)}")
        vae.to("cpu")
        te1.to("cpu")
        te2.to("cpu")
        if device.type == "cuda":
            torch.cuda.empty_cache()

        # ---- LoRA adapters on the UNet attention projections
        unet.to(device)
        unet.add_adapter(
            LoraConfig(
                r=req.rank,
                lora_alpha=req.rank,
                init_lora_weights="gaussian",
                target_modules=["to_k", "to_q", "to_v", "to_out.0"],
            )
        )
        if self.dtype != torch.float32:
            cast_training_params(unet, dtype=torch.float32)  # trainable LoRA params in fp32
        unet.enable_gradient_checkpointing()
        unet.train()
        params = [p for p in unet.parameters() if p.requires_grad]
        optimizer = torch.optim.AdamW(params, lr=req.learning_rate, weight_decay=1e-2)
        # fp16 activations need loss scaling (bf16 does not); fp32 LoRA weights receive unscaled gradients.
        scaler = torch.amp.GradScaler("cuda", enabled=use_amp and self.dtype == torch.float16)
        time_ids = torch.tensor([[res, res, 0, 0, res, res]], device=device, dtype=self.dtype)
        prediction = scheduler.config.get("prediction_type", "epsilon")

        for step in range(1, req.steps + 1):
            idx = random.randrange(len(latents_cache))
            latents = latents_cache[idx].to(device, dtype=self.dtype)
            prompt_embeds, pooled = embeds_cache[idx]
            noise = torch.randn_like(latents)
            t = torch.randint(0, scheduler.config.num_train_timesteps, (1,), device=device).long()
            noisy = scheduler.add_noise(latents, noise, t)
            with torch.autocast(device.type, dtype=self.dtype, enabled=use_amp):
                pred = unet(
                    noisy,
                    t,
                    encoder_hidden_states=prompt_embeds,
                    added_cond_kwargs={"text_embeds": pooled, "time_ids": time_ids},
                ).sample
            target = noise if prediction == "epsilon" else scheduler.get_velocity(latents, noise, t)
            loss = F.mse_loss(pred.float(), target.float())
            scaler.scale(loss).backward()
            scaler.unscale_(optimizer)
            torch.nn.utils.clip_grad_norm_(params, 1.0)
            scaler.step(optimizer)
            scaler.update()
            optimizer.zero_grad(set_to_none=True)
            req.progress(step, req.steps, f"training LoRA · loss {loss.item():.4f}")

        out_dir = req.work_dir / "output"
        out_dir.mkdir(parents=True, exist_ok=True)
        unet_lora = convert_state_dict_to_diffusers(get_peft_model_state_dict(unet))
        StableDiffusionXLPipeline.save_lora_weights(
            out_dir, unet_lora_layers=unet_lora, safe_serialization=True
        )
        del pipe, unet, vae, te1, te2, optimizer
        if device.type == "cuda":
            torch.cuda.empty_cache()
        return out_dir / "pytorch_lora_weights.safetensors"

    def _encode(self, caption: str, tok1: Any, tok2: Any, te1: Any, te2: Any, device: Any) -> tuple[Any, Any]:
        """SDXL prompt encoding: penultimate hidden states of both encoders, pooled output of the second."""
        import torch

        hidden = []
        pooled = None
        for tok, te in ((tok1, te1), (tok2, te2)):
            ids = tok(
                caption,
                padding="max_length",
                max_length=tok.model_max_length,
                truncation=True,
                return_tensors="pt",
            ).input_ids.to(device)
            out = te(ids, output_hidden_states=True)
            pooled = out[0]  # CLIPTextModelWithProjection → text_embeds for the second encoder
            hidden.append(out.hidden_states[-2])
        prompt_embeds = torch.cat(hidden, dim=-1).to(self.dtype)
        return prompt_embeds, pooled.to(self.dtype)  # type: ignore[union-attr]
