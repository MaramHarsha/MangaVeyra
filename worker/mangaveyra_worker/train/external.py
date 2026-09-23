"""Run an external LoRA trainer from a command template (``MV_TRAIN_COMMAND``).

The template is split like a shell command *before* substitution, so values (trigger words, paths) can never inject
extra arguments. Placeholders:

``{image_dir}`` (kohya ``<repeats>_<trigger>`` folder), ``{dataset_dir}`` (its parent), ``{out_dir}``,
``{output_name}``, ``{steps}``, ``{rank}``, ``{alpha}``, ``{lr}``, ``{resolution}``, ``{seed}``, ``{trigger}``,
``{base_model}`` (the model id) and ``{base_path}`` (repo id or checkpoint path).

Example for kohya-ss sd-scripts::

    MV_TRAIN_COMMAND="accelerate launch /opt/sd-scripts/sdxl_train_network.py
      --pretrained_model_name_or_path={base_path} --train_data_dir={dataset_dir}
      --output_dir={out_dir} --output_name={output_name} --max_train_steps={steps}
      --network_module=networks.lora --network_dim={rank} --network_alpha={alpha}
      --learning_rate={lr} --resolution={resolution} --seed={seed}
      --mixed_precision=bf16 --save_model_as=safetensors --caption_extension=.txt --cache_latents
      --gradient_checkpointing --optimizer_type=AdamW8bit"

Progress is parsed from the trainer's output (``123/1000`` counters such as tqdm's, or ``step 123``); the newest
``*.safetensors`` under ``{out_dir}`` is the result.
"""

from __future__ import annotations

import os
import re
import shlex
import signal
import subprocess
import threading
import time
from collections import deque
from pathlib import Path

from mangaveyra_worker.errors import JobCanceled, JobFailure, OutOfMemory
from mangaveyra_worker.train.base import Trainer, TrainRequest, write_dataset

_COUNTER = re.compile(r"(?<![\d.])(\d{1,7})\s*/\s*(\d{1,7})(?![\d.])")
_STEP = re.compile(r"\bstep[s]?\s*[:=]?\s*(\d{1,7})\b", re.IGNORECASE)


def parse_progress(line: str, steps: int) -> int | None:
    """Extract the current step from a trainer log line, or ``None``."""
    best = None
    for m in _COUNTER.finditer(line):
        cur, total = int(m.group(1)), int(m.group(2))
        if 0 < total and cur <= total and (total == steps or best is None):
            best = round(cur * steps / total) if total != steps else cur
    if best is not None:
        return best
    m = _STEP.search(line)
    if m and int(m.group(1)) <= steps:
        return int(m.group(1))
    return None


class ExternalTrainer(Trainer):
    """Runs a user-supplied training command."""

    name = "external"

    def __init__(
        self, template: str, *, timeout_s: float = 6 * 3600, env: dict[str, str] | None = None
    ) -> None:
        if not template.strip():
            raise ValueError("empty MV_TRAIN_COMMAND")
        self.argv_template = shlex.split(template)
        self.timeout_s = timeout_s
        self.env = env

    def command(self, req: TrainRequest, image_dir: Path, out_dir: Path, output_name: str) -> list[str]:
        """The argv for this request."""
        values = {
            "image_dir": str(image_dir),
            "dataset_dir": str(image_dir.parent),
            "out_dir": str(out_dir),
            "output_name": output_name,
            "steps": str(req.steps),
            "rank": str(req.rank),
            "alpha": str(max(1, req.rank // 2)),
            "lr": f"{req.learning_rate:g}",
            "resolution": str(req.resolution),
            "seed": str(req.seed),
            "trigger": req.trigger,
            "base_model": req.base_model,
            "base_path": req.base_path,
        }
        try:
            return [arg.format(**values) for arg in self.argv_template]
        except (KeyError, IndexError, ValueError) as exc:
            raise JobFailure(f"MV_TRAIN_COMMAND has an unknown placeholder: {exc}", retryable=False) from exc

    def train(self, req: TrainRequest) -> Path:
        """Prepare the dataset, run the command, stream progress, return the newest LoRA file."""
        image_dir = write_dataset(req)
        out_dir = req.work_dir / "output"
        out_dir.mkdir(parents=True, exist_ok=True)
        name = "".join(c if c.isalnum() or c in "-_" else "_" for c in req.trigger) or "character"
        argv = self.command(req, image_dir, out_dir, name)
        env = {**os.environ, **(self.env or {}), "PYTHONUNBUFFERED": "1"}
        try:
            proc = subprocess.Popen(
                argv,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                cwd=req.work_dir,
                env=env,
                start_new_session=True,
                bufsize=0,
            )
        except OSError as exc:
            raise JobFailure(f"cannot start trainer {argv[0]!r}: {exc}", retryable=False) from exc
        tail: deque[str] = deque(maxlen=30)
        reader = threading.Thread(target=self._read, args=(proc, req, tail), daemon=True)
        reader.start()
        deadline = time.monotonic() + self.timeout_s
        try:
            while proc.poll() is None:
                if time.monotonic() > deadline:
                    raise JobFailure("trainer timed out", code="timeout", retryable=True)
                req.progress(-1, req.steps, "")  # cancel check; -1 = no new step
                time.sleep(0.5)
        except (JobCanceled, JobFailure):
            _kill(proc)
            raise
        reader.join(timeout=5)
        output = "\n".join(tail)
        if proc.returncode != 0:
            if "out of memory" in output.lower():
                raise OutOfMemory(f"trainer ran out of memory: {output[-600:]}")
            raise JobFailure(f"trainer exited with code {proc.returncode}: {output[-800:]}", retryable=False)
        files = sorted(out_dir.rglob("*.safetensors"), key=lambda p: p.stat().st_mtime)
        if not files:
            raise JobFailure(f"trainer finished but wrote no .safetensors to {out_dir}", retryable=False)
        return files[-1]

    @staticmethod
    def _read(proc: subprocess.Popen[bytes], req: TrainRequest, tail: deque[str]) -> None:
        assert proc.stdout is not None
        buf = b""
        last = -1
        while True:
            chunk = proc.stdout.read(4096)
            if not chunk:
                break
            buf += chunk
            # tqdm redraws with \r; treat it as a line break.
            parts = re.split(rb"[\r\n]", buf)
            buf = parts.pop()
            for raw in parts:
                line = raw.decode("utf-8", "replace").strip()
                if not line:
                    continue
                tail.append(line)
                step = parse_progress(line, req.steps)
                if step is not None and step != last:
                    last = step
                    try:
                        req.progress(step, req.steps, "training LoRA")
                    except Exception:  # cancel is handled by the main loop
                        pass


def _kill(proc: subprocess.Popen[bytes]) -> None:
    """Terminate the trainer's whole process group (accelerate spawns children)."""
    try:
        os.killpg(proc.pid, signal.SIGTERM)
        proc.wait(timeout=15)
    except (ProcessLookupError, subprocess.TimeoutExpired, PermissionError):
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
