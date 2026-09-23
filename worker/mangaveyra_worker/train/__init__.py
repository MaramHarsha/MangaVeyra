"""Character LoRA training for ``character.train``.

Two trainers implement the same :class:`~mangaveyra_worker.train.base.Trainer` interface:

* :class:`~mangaveyra_worker.train.external.ExternalTrainer` runs any command (kohya-ss ``sdxl_train_network.py``,
  SimpleTuner, ai-toolkit, OneTrainer …) from the ``MV_TRAIN_COMMAND`` template.
* :class:`~mangaveyra_worker.train.lora_sdxl.PeftSdxlTrainer` is a compact built-in SDXL/Illustrious LoRA trainer
  on diffusers + peft.
"""
