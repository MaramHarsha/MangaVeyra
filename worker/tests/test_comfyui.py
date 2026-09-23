"""ComfyUI backend against a fake ComfyUI server."""

from __future__ import annotations

import io
import json
from pathlib import Path
from typing import Any, Callable

import pytest
from PIL import Image

from mangaveyra_worker.backends.comfyui import ComfyUIBackend, WorkflowBundle, load_bundles
from mangaveyra_worker.config import Config
from mangaveyra_worker.errors import BadInput, JobFailure
from mangaveyra_worker.jobs import Job
from mangaveyra_worker.manifests import ModelCatalog
from tests.conftest import png
from tests.fakes import FakeComfyUI

WORKFLOW = {
    "3": {
        "class_type": "KSampler",
        "inputs": {
            "seed": 0,
            "steps": 20,
            "cfg": 7.0,
            "denoise": 1.0,
            "model": ["4", 0],
            "positive": ["6", 0],
            "negative": ["7", 0],
            "latent_image": ["5", 0],
        },
    },
    "4": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "illustrious.safetensors"}},
    "5": {"class_type": "EmptyLatentImage", "inputs": {"width": 512, "height": 512, "batch_size": 1}},
    "6": {"class_type": "CLIPTextEncode", "inputs": {"text": "", "clip": ["4", 1]}},
    "7": {"class_type": "CLIPTextEncode", "inputs": {"text": "", "clip": ["4", 1]}},
    "8": {"class_type": "VAEDecode", "inputs": {"samples": ["3", 0], "vae": ["4", 2]}},
    "9": {"class_type": "SaveImage", "inputs": {"images": ["8", 0], "filename_prefix": "mv"}},
    "10": {"class_type": "LoadImage", "inputs": {"image": ""}},
}

BUNDLE = {
    "model": "illustrious-comfy",
    "job_types": ["image.generate", "image.edit"],
    "workflow_file": "illustrious.api.json",
    "inputs": {
        "prompt": "6.inputs.text",
        "negative": "7.inputs.text",
        "seed": "3.inputs.seed",
        "steps": "3.inputs.steps",
        "guidance": "3.inputs.cfg",
        "width": "5.inputs.width",
        "height": "5.inputs.height",
        "strength": "3.inputs.denoise",
        "image": "10.inputs.image",
    },
    "templates": {"prompt": "{prompt}, monochrome manga"},
    "outputs": ["9"],
}


@pytest.fixture
def workflows(tmp_path: Path) -> Path:
    d = tmp_path / "workflows"
    d.mkdir()
    (d / "illustrious.api.json").write_text(json.dumps(WORKFLOW))
    (d / "illustrious.json").write_text(json.dumps(BUNDLE))
    return d


def backend(url: str, workflows: Path) -> ComfyUIBackend:
    return ComfyUIBackend(Config(comfyui_url=url, comfyui_workflows_dir=workflows, comfyui_timeout_s=10), [])


def test_bundle_loading_and_advertising(fake_comfy: FakeComfyUI, workflows: Path) -> None:
    b = backend(fake_comfy.url, workflows)
    assert b.models() == ["illustrious-comfy"]
    assert set(b.job_types()) == {"image.generate", "image.edit"}
    assert b.accepts(
        Job(
            "j",
            "image.generate",
            model=__import__("mangaveyra_worker.jobs").jobs.ModelRef("illustrious-comfy"),
        )
    )
    assert not b.accepts(Job("j", "image.inpaint"))


def test_generate_maps_inputs_and_uploads_outputs(
    fake_comfy: FakeComfyUI, workflows: Path, run_job: Callable[..., Any]
) -> None:
    b = backend(fake_comfy.url, workflows)
    result, client = run_job(
        b,
        "image.generate",
        {
            "prompt": "Rin Asagiri",
            "negative": "blurry",
            "seed": 300,
            "n": 2,
            "steps": 12,
            "guidance": 5.5,
            "width": 768,
            "height": 1024,
        },
        model="illustrious-comfy",
    )
    assert [i["seed"] for i in result.output["images"]] == [300, 301]
    first = fake_comfy.prompts["p1"]
    assert first["6"]["inputs"]["text"] == "Rin Asagiri, monochrome manga"
    assert first["7"]["inputs"]["text"] == "blurry"
    assert first["3"]["inputs"]["seed"] == 300 and fake_comfy.prompts["p2"]["3"]["inputs"]["seed"] == 301
    assert first["3"]["inputs"]["steps"] == 12 and first["3"]["inputs"]["cfg"] == 5.5
    assert (first["5"]["inputs"]["width"], first["5"]["inputs"]["height"]) == (768, 1024)
    assert WORKFLOW["6"]["inputs"]["text"] == ""  # the template is never mutated
    img = Image.open(io.BytesIO(client.uploads[result.output["images"][1]["key"]]["data"]))
    assert img.getpixel((0, 0))[:2] == (301 % 256, 301 // 256)  # the output of the right prompt
    assert b"workflow" not in client.uploads[result.output["images"][0]["key"]]["data"]


def test_edit_uploads_input_image(
    fake_comfy: FakeComfyUI, workflows: Path, run_job: Callable[..., Any]
) -> None:
    b = backend(fake_comfy.url, workflows)
    run_job(
        b,
        "image.edit",
        {"prompt": "rain", "strength": 0.4, "seed": 1},
        {"image": png(Image.new("RGB", (32, 32), "red"))},
        model="illustrious-comfy",
    )
    uploaded = list(fake_comfy.uploads)
    assert len(uploaded) == 1 and uploaded[0].endswith("-image.png")
    assert fake_comfy.prompts["p1"]["10"]["inputs"]["image"] == uploaded[0]
    assert fake_comfy.prompts["p1"]["3"]["inputs"]["denoise"] == 0.4


def test_execution_error_and_validation_error(workflows: Path, run_job: Callable[..., Any]) -> None:
    comfy = FakeComfyUI(fail_with="CUDA error: out of memory").start()
    try:
        with pytest.raises(JobFailure) as err:
            run_job(backend(comfy.url, workflows), "image.generate", {"seed": 1}, model="illustrious-comfy")
        assert err.value.code == "oom"
    finally:
        comfy.stop()
    broken = dict(WORKFLOW, **{"11": {"class_type": "Broken", "inputs": {}}})
    (workflows / "illustrious.api.json").write_text(json.dumps(broken))
    comfy = FakeComfyUI().start()
    try:
        with pytest.raises(BadInput, match="rejected"):
            run_job(backend(comfy.url, workflows), "image.generate", {"seed": 1}, model="illustrious-comfy")
    finally:
        comfy.stop()


def test_cancel_removes_pending_prompt(workflows: Path, run_job: Callable[..., Any]) -> None:
    comfy = FakeComfyUI(delay_s=30).start()
    try:
        from mangaveyra_worker.errors import JobCanceled

        with pytest.raises(JobCanceled):
            run_job(
                backend(comfy.url, workflows),
                "image.generate",
                {"seed": 1},
                model="illustrious-comfy",
                cancel_after=2,
            )
        assert comfy.deleted == ["p1"]
    finally:
        comfy.stop()


def test_bundle_validation(tmp_path: Path) -> None:
    with pytest.raises(ValueError, match="API format"):
        WorkflowBundle({"workflow": {"nodes": [], "links": []}, "inputs": {}}, source="ui.json")
    with pytest.raises(ValueError, match="not found"):
        WorkflowBundle({"workflow": WORKFLOW, "inputs": {"prompt": "99.inputs.text"}}, source="x.json")
    with pytest.raises(ValueError, match="NODE.inputs.NAME"):
        WorkflowBundle({"workflow": WORKFLOW, "inputs": {"prompt": "6.text"}}, source="x.json")
    with pytest.raises(ValueError, match="image job types"):
        WorkflowBundle({"workflow": WORKFLOW, "inputs": {}, "job_types": ["ocr"]}, source="x.json")


def test_manifest_inline_bundle(tmp_path: Path) -> None:
    catalog = ModelCatalog(
        [
            {
                "id": "flux-comfy",
                "worker": {"comfyui": {"workflow": WORKFLOW, "inputs": {"prompt": "6.inputs.text"}}},
            }
        ]
    )
    bundles = load_bundles(None, catalog)
    assert list(bundles) == ["flux-comfy"]
    assert bundles["flux-comfy"].render({"prompt": "x"})["6"]["inputs"]["text"] == "x"


def test_unavailable_without_url() -> None:
    assert ComfyUIBackend.available(Config())[0] is False


def test_websocket_progress_and_previews(
    fake_comfy: FakeComfyUI, workflows: Path, run_job: Callable[..., Any], monkeypatch: pytest.MonkeyPatch
) -> None:
    ws_sync = pytest.importorskip("websockets.sync.server")
    import threading

    preview = io.BytesIO()
    Image.new("RGB", (16, 16), "blue").save(preview, format="JPEG")

    def handler(conn: Any) -> None:
        conn.send(json.dumps({"type": "status", "data": {"status": {"exec_info": {"queue_remaining": 1}}}}))
        conn.send(json.dumps({"type": "execution_start", "data": {"prompt_id": "p1"}}))
        for v in range(1, 5):
            conn.send(json.dumps({"type": "progress", "data": {"value": v, "max": 4, "prompt_id": "p1"}}))
        conn.send((1).to_bytes(4, "big") + (1).to_bytes(4, "big") + preview.getvalue())
        conn.send(json.dumps({"type": "progress", "data": {"value": 2, "max": 4, "prompt_id": "other"}}))
        conn.send(json.dumps({"type": "executing", "data": {"node": None, "prompt_id": "p1"}}))
        try:
            conn.recv(timeout=2)
        except Exception:
            pass

    server = ws_sync.serve(handler, "127.0.0.1", 0)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    port = server.socket.getsockname()[1]
    monkeypatch.setattr(
        "mangaveyra_worker.backends.comfyui.ComfyUIClient.ws_url", lambda self: f"ws://127.0.0.1:{port}/ws"
    )
    try:
        result, client = run_job(
            backend(fake_comfy.url, workflows), "image.generate", {"seed": 5}, model="illustrious-comfy"
        )
    finally:
        server.shutdown()
    assert len(result.output["images"]) == 1
    steps = [c.get("step") for c in client.progress_calls if c.get("message") == "denoising"]
    assert [s for s in steps if s is not None] == [1, 2, 3, 4]  # the other prompt's progress is ignored
    assert any(c.get("preview_png_b64") for c in client.progress_calls)


def test_shipped_example_bundle_is_valid() -> None:
    examples = Path(__file__).resolve().parents[1] / "examples" / "comfyui"
    bundles = load_bundles(examples, ModelCatalog())
    assert list(bundles) == ["illustrious-comfy"]
    wf = bundles["illustrious-comfy"].render({"prompt": "Rin", "seed": 9, "width": 832})
    assert wf["3"]["inputs"]["seed"] == 9 and wf["5"]["inputs"]["width"] == 832
    assert wf["6"]["inputs"]["text"].startswith("Rin, masterpiece")


def test_example_model_manifest_is_valid() -> None:
    from mangaveyra_worker.diffusion.spec import ModelSpec

    examples = Path(__file__).resolve().parents[1] / "examples" / "models"
    catalog = ModelCatalog.load(examples)
    m = catalog.get("my-sdxl-finetune")
    spec = ModelSpec.from_manifest("my-sdxl-finetune", m["worker"]["diffusers"], m)
    assert spec.single_file and spec.trainable
