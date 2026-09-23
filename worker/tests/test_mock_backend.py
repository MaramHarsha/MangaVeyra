"""Mock backend outputs: sizes, determinism, variations, scope preservation, vision helpers."""

from __future__ import annotations

import hashlib
import io
from typing import Any, Callable

import pytest
from PIL import Image, ImageChops, ImageDraw

from mangaveyra_worker.backends.mock import MockBackend
from mangaveyra_worker.config import Config
from mangaveyra_worker.errors import BadInput, JobCanceled
from mangaveyra_worker.imaging import read_safetensors_metadata
from tests.conftest import png, three_panel_page


@pytest.fixture
def mock() -> MockBackend:
    return MockBackend(Config(mock_step_delay=0.0), [])


def _img(client: Any, key: str) -> Image.Image:
    return Image.open(io.BytesIO(client.uploads[key]["data"]))


def _sha(client: Any, key: str) -> str:
    return hashlib.sha256(client.uploads[key]["data"]).hexdigest()


def test_generate_sizes_and_seeds(mock: MockBackend, run_job: Callable[..., Any]) -> None:
    result, client = run_job(
        mock,
        "image.generate",
        {"prompt": "Kaito Mbeki at the lighthouse", "width": 320, "height": 448, "n": 4, "seed": 7},
    )
    images = result.output["images"]
    assert [i["seed"] for i in images] == [7, 8, 9, 10]
    assert {_img(client, i["key"]).size for i in images} == {(320, 448)}
    assert len({_sha(client, i["key"]) for i in images}) == 4  # variations differ
    assert result.gpu_seconds == 0.0
    meta = client.uploads[images[0]["key"]]["meta"]
    assert meta["placeholder"] is True and meta["seed"] == 7


def test_generate_is_deterministic(mock: MockBackend, run_job: Callable[..., Any]) -> None:
    inp = {
        "prompt": "Old Wren mends a net",
        "width": 256,
        "height": 256,
        "n": 2,
        "seed": 1234,
        "style": "colour",
    }
    a, ca = run_job(mock, "image.generate", inp)
    b, cb = run_job(mock, "image.generate", inp)
    assert [_sha(ca, i["key"]) for i in a.output["images"]] == [
        _sha(cb, i["key"]) for i in b.output["images"]
    ]
    c, cc = run_job(mock, "image.generate", {**inp, "seed": 1235})
    assert _sha(cc, c.output["images"][0]["key"]) != _sha(ca, a.output["images"][0]["key"])


def test_random_seed_is_reported(mock: MockBackend, run_job: Callable[..., Any]) -> None:
    result, _ = run_job(mock, "image.generate", {"width": 64, "height": 64, "seed": None})
    assert isinstance(result.output["images"][0]["seed"], int)


@pytest.mark.parametrize("style", ["bw_screentone", "flat_grey"])
def test_monochrome_styles_are_grey(mock: MockBackend, run_job: Callable[..., Any], style: str) -> None:
    result, client = run_job(mock, "image.generate", {"width": 128, "height": 96, "seed": 1, "style": style})
    img = _img(client, result.output["images"][0]["key"]).convert("RGB")
    r, g, b = img.split()
    assert ImageChops.difference(r, g).getbbox() is None and ImageChops.difference(g, b).getbbox() is None


def test_generate_with_pose_json(mock: MockBackend, run_job: Callable[..., Any]) -> None:
    pose = {
        "canvas_width": 100,
        "canvas_height": 100,
        "people": [
            {
                "pose_keypoints_2d": [
                    50,
                    10,
                    1,
                    50,
                    30,
                    1,
                    35,
                    30,
                    1,
                    30,
                    50,
                    1,
                    28,
                    70,
                    1,
                    65,
                    30,
                    1,
                    70,
                    50,
                    1,
                    72,
                    70,
                    1,
                ]
                + [0, 0, 0] * 10
            }
        ],
    }
    result, client = run_job(mock, "image.generate", {"width": 200, "height": 200, "seed": 3, "pose": pose})
    img = _img(client, result.output["images"][0]["key"]).convert("RGB")
    colours = {img.getpixel((x, 60)) for x in range(60, 140)}
    assert any(c[0] > 200 and c[1] < 120 for c in colours)  # coloured OpenPose limbs are visible


@pytest.mark.parametrize("bad", [{"width": 10}, {"n": 9}, {"steps": "many"}, {"seed": "abc"}])
def test_generate_rejects_bad_input(
    mock: MockBackend, run_job: Callable[..., Any], bad: dict[str, Any]
) -> None:
    with pytest.raises(BadInput):
        run_job(mock, "image.generate", bad)


def test_edit_keeps_size(mock: MockBackend, run_job: Callable[..., Any]) -> None:
    src = Image.new("RGB", (300, 200), (240, 240, 240))
    result, client = run_job(
        mock, "image.edit", {"prompt": "add rain", "n": 2, "seed": 5, "strength": 0.4}, {"image": png(src)}
    )
    assert [_img(client, i["key"]).size for i in result.output["images"]] == [(300, 200)] * 2


def test_inpaint_changes_only_masked_pixels(mock: MockBackend, run_job: Callable[..., Any]) -> None:
    src = Image.new("RGB", (200, 200), (10, 200, 30))
    mask = Image.new("L", (200, 200), 0)
    ImageDraw.Draw(mask).rectangle((50, 50, 149, 149), fill=255)
    result, client = run_job(
        mock,
        "image.inpaint",
        {"prompt": "a gull", "seed": 2, "feather_px": 0},
        {"image": png(src), "mask": png(mask)},
    )
    out = _img(client, result.output["images"][0]["key"]).convert("RGB")
    diff = ImageChops.difference(out, src).getbbox()
    assert diff is not None
    x0, y0, x1, y1 = diff
    assert x0 >= 50 and y0 >= 50 and x1 <= 150 and y1 <= 150


def test_sketch_and_sheet_and_upscale(mock: MockBackend, run_job: Callable[..., Any]) -> None:
    sketch = Image.new("RGB", (240, 180), "white")
    ImageDraw.Draw(sketch).line((10, 10, 230, 170), fill="black", width=4)
    r, c = run_job(
        mock, "sketch.ink", {"strength": 0.2, "finish": "flat_grey", "seed": 1}, {"sketch": png(sketch)}
    )
    assert _img(c, r.output["images"][0]["key"]).size == (240, 180)
    r, c = run_job(mock, "character.sheet", {"prompt": "Sora Vell", "n": 2, "seed": 9})
    assert [_img(c, i["key"]).size for i in r.output["images"]] == [(1536, 1024)] * 2
    r, c = run_job(mock, "upscale", {"scale": 4}, {"image": png(Image.new("RGB", (50, 40)))})
    assert _img(c, r.output["images"][0]["key"]).size == (200, 160)
    with pytest.raises(BadInput):
        run_job(mock, "upscale", {"scale": 3}, {"image": png(Image.new("RGB", (50, 40)))})


def test_character_score_is_plausible_and_deterministic(
    mock: MockBackend, run_job: Callable[..., Any]
) -> None:
    ref = png(Image.new("RGB", (32, 32), (1, 2, 3)))
    other = png(Image.new("RGB", (32, 32), (200, 2, 3)))
    files = {
        "reference_0": ref,
        "candidate_0": ref,
        "candidate_1": other,
        "candidate_2": png(Image.new("RGB", (8, 8))),
    }
    a, _ = run_job(mock, "character.score", {"threshold": 0.8}, files)
    b, _ = run_job(mock, "character.score", {"threshold": 0.8}, files)
    assert a.output["scores"] == b.output["scores"]
    assert a.output["scores"][0] == 0.99
    assert all(0.72 <= s <= 0.97 for s in a.output["scores"][1:])
    with pytest.raises(BadInput):
        run_job(mock, "character.score", {}, {"reference_0": ref})


def test_character_train_outputs_lora_and_tests(mock: MockBackend, run_job: Callable[..., Any]) -> None:
    refs = {f"ref_{i}": png(Image.new("RGB", (64, 64), (i * 40, 10, 10))) for i in range(3)}
    result, client = run_job(
        mock,
        "character.train",
        {"trigger": "rinasagiri", "base_model": "illustrious-xl", "steps": 500, "rank": 8},
        refs,
    )
    out = result.output
    lora = client.uploads[out["lora"]["key"]]
    header = read_safetensors_metadata(lora["data"])
    assert header["__metadata__"]["trigger"] == "rinasagiri"
    assert lora["filename"] == "rinasagiri.safetensors"
    assert len(out["test_images"]) == 2
    assert 0.86 <= out["score"] <= 0.96
    with pytest.raises(BadInput):
        run_job(mock, "character.train", {"trigger": ""}, refs)


def test_detect_panels_on_synthetic_page(mock: MockBackend, run_job: Callable[..., Any]) -> None:
    result, _ = run_job(mock, "detect.panels", {"reading": "rtl"}, {"page": png(three_panel_page())})
    panels = result.output["panels"]
    assert len(panels) == 3
    assert [p["order"] for p in panels] == [1, 2, 3]
    xs = [min(pt[0] for pt in p["points"]) for p in panels]
    assert xs[1] > xs[2]  # rtl: bottom-right before bottom-left
    assert "bubbles" in result.output


def test_ocr_placeholder_without_engine(mock: MockBackend, run_job: Callable[..., Any]) -> None:
    result, _ = run_job(
        MockBackend(Config(mock_step_delay=0.0, ocr_engine="none"), []),
        "ocr",
        {"lang": "en"},
        {"image": png(Image.new("RGB", (100, 50), "white"))},
    )
    assert result.output["engine"] == "mock"
    assert result.output["lines"][0]["box"] == [0, 0, 100, 50]


def test_cancel_stops_mock(run_job: Callable[..., Any]) -> None:
    slow = MockBackend(Config(mock_step_delay=0.001), [])
    with pytest.raises(JobCanceled):
        run_job(slow, "image.generate", {"width": 64, "height": 64, "n": 4, "steps": 100}, cancel_after=5)


def test_bad_image_is_bad_input(mock: MockBackend, run_job: Callable[..., Any]) -> None:
    with pytest.raises(BadInput, match="not a readable image"):
        run_job(mock, "image.edit", {}, {"image": b"definitely not a png"})
