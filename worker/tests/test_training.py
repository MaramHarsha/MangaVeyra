"""External LoRA trainer (runs without torch) and dataset preparation."""

from __future__ import annotations

import sys
import threading
import time
from pathlib import Path

import pytest
from PIL import Image

from mangaveyra_worker.errors import JobCanceled, JobFailure, OutOfMemory
from mangaveyra_worker.train.base import TrainRequest, write_dataset
from mangaveyra_worker.train.external import ExternalTrainer, parse_progress

FAKE_TRAINER = r"""
import argparse, pathlib, sys, time
p = argparse.ArgumentParser()
p.add_argument("--data"); p.add_argument("--out"); p.add_argument("--name"); p.add_argument("--steps", type=int)
p.add_argument("--rank"); p.add_argument("--mode", default="ok")
a = p.parse_args()
imgs = sorted(pathlib.Path(a.data).glob("*/*.png"))
assert imgs, "no dataset"
if a.mode == "oom":
    print("RuntimeError: CUDA out of memory. Tried to allocate 2.00 GiB"); sys.exit(1)
if a.mode == "fail":
    print("something broke"); sys.exit(3)
for s in range(1, a.steps + 1):
    sys.stdout.write(f"\rsteps: {int(100*s/a.steps)}%| {s}/{a.steps} [00:01<00:00, loss=0.1]"); sys.stdout.flush()
    time.sleep(0.2 if a.mode == "slow" else 0.001)
print()
(pathlib.Path(a.out) / f"{a.name}.safetensors").write_bytes(b"LORA" + a.rank.encode())
"""


def request(
    tmp_path: Path, calls: list[tuple[int, int, str]], cancel: threading.Event | None = None
) -> TrainRequest:
    def progress(step: int, steps: int, message: str) -> None:
        if cancel is not None and cancel.is_set():
            raise JobCanceled("job canceled (server)")
        calls.append((step, steps, message))

    images = [Image.new("RGB", (80 + i, 120), (i * 50, 20, 20)) for i in range(3)]
    return TrainRequest(
        trigger="kaito mbeki",
        images=images,
        steps=20,
        rank=8,
        base_model="illustrious-xl",
        base_path="OnomaAIResearch/Illustrious-xl-early-release-v0",
        work_dir=tmp_path,
        resolution=64,
        progress=progress,
    )


def trainer(tmp_path: Path, mode: str = "ok") -> ExternalTrainer:
    script = tmp_path / "fake_train.py"
    script.write_text(FAKE_TRAINER)
    return ExternalTrainer(
        f"{sys.executable} {script} --data {{dataset_dir}} --out {{out_dir}} --name {{output_name}}"
        f" --steps {{steps}} --rank {{rank}} --mode {mode}"
    )


def test_dataset_layout(tmp_path: Path) -> None:
    folder = write_dataset(request(tmp_path, []))
    assert folder.name == "10_kaito_mbeki"
    assert sorted(p.name for p in folder.glob("*.png")) == ["0000.png", "0001.png", "0002.png"]
    assert (folder / "0000.txt").read_text().strip() == "kaito mbeki, 1character, solo"
    assert Image.open(folder / "0001.png").size == (64, 64)
    assert (folder / "metadata.jsonl").read_text().count("\n") == 3


def test_external_trainer_success(tmp_path: Path) -> None:
    calls: list[tuple[int, int, str]] = []
    path = trainer(tmp_path).train(request(tmp_path, calls))
    assert path.name == "kaito_mbeki.safetensors" and path.read_bytes() == b"LORA8"
    steps = [c[0] for c in calls if c[0] >= 0]
    assert steps[-1] == 20 and steps == sorted(steps)


def test_external_trainer_failures(tmp_path: Path) -> None:
    with pytest.raises(OutOfMemory):
        trainer(tmp_path, "oom").train(request(tmp_path, []))
    with pytest.raises(JobFailure, match="exited with code 3"):
        trainer(tmp_path, "fail").train(request(tmp_path, []))
    with pytest.raises(JobFailure, match="cannot start"):
        ExternalTrainer("/nonexistent/trainer --x").train(request(tmp_path, []))
    with pytest.raises(JobFailure, match="placeholder"):
        ExternalTrainer("train --x {nope}").train(request(tmp_path, []))


def test_external_trainer_cancel_kills_process(tmp_path: Path) -> None:
    cancel = threading.Event()
    threading.Timer(0.8, cancel.set).start()
    started = time.monotonic()
    with pytest.raises(JobCanceled):
        trainer(tmp_path, "slow").train(request(tmp_path, [], cancel))
    assert time.monotonic() - started < 3.5  # the 20 × 0.2 s run was stopped early


def test_template_values_cannot_inject_arguments(tmp_path: Path) -> None:
    req = request(tmp_path, [])
    req.trigger = "rin; rm -rf / --evil"
    argv = ExternalTrainer("train --trigger {trigger} --steps {steps}").command(req, tmp_path, tmp_path, "x")
    assert argv == ["train", "--trigger", "rin; rm -rf / --evil", "--steps", "20"]


@pytest.mark.parametrize(
    ("line", "expected"),
    [
        ("steps:  45%|████▌     | 450/1000 [01:10<01:25, loss=0.0912]", 450),
        ("epoch 1/10", None),  # a counter for another total is scaled, not trusted as the step
        ("step 12 loss 0.1", 12),
        ("nothing here", None),
    ],
)
def test_parse_progress(line: str, expected: int | None) -> None:
    got = parse_progress(line, 1000)
    if expected is None and "epoch" in line:
        assert got == 100  # 1/10 of the run
    else:
        assert got == expected
