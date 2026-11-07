"""Image helpers."""

from __future__ import annotations

import struct

from PIL import Image

from mangaveyra_worker import imaging
from mangaveyra_worker.jobs import decode_image, encode_png, indexed_names, preview_b64


def test_placeholder_label_and_size() -> None:
    img = imaging.placeholder(300, 200, seed=1, job_type="image.generate", prompt="Pip the Gull")
    assert img.size == (300, 200) and img.mode == "RGB"


def test_openpose_normalised_and_absolute() -> None:
    kp = [[0.5, 0.1], [0.5, 0.3], [0.4, 0.3], [0.35, 0.5]]
    a = imaging.render_openpose({"keypoints": kp}, 100, 100)
    b = imaging.render_openpose(
        {"canvas_width": 10, "canvas_height": 10, "keypoints": [[x * 10, y * 10] for x, y in kp]}, 100, 100
    )
    assert a.getbbox() is not None
    assert a.tobytes() == b.tobytes()


def test_safetensors_roundtrip() -> None:
    blob = imaging.write_safetensors({"w": ("F32", [2], struct.pack("<2f", 1.0, 2.0))}, {"k": "v"})
    header = imaging.read_safetensors_metadata(blob)
    assert header["__metadata__"] == {"k": "v"}
    assert header["w"]["data_offsets"] == [0, 8]
    n = struct.unpack("<Q", blob[:8])[0]
    assert n % 8 == 0 and struct.unpack("<2f", blob[8 + n :]) == (1.0, 2.0)


def test_png_is_deterministic_and_decodes() -> None:
    img = imaging.placeholder(64, 64, seed=3, job_type="t")
    assert encode_png(img) == encode_png(img)
    assert decode_image(encode_png(img)).size == (64, 64)


def test_rgba_is_flattened_on_white() -> None:
    rgba = Image.new("RGBA", (4, 4), (0, 0, 0, 0))
    assert decode_image(encode_png(rgba)).getpixel((0, 0)) == (255, 255, 255)


def test_preview_fits_protocol_limit() -> None:
    import random

    rng = random.Random(0)
    noisy = Image.frombytes("RGB", (1024, 1024), bytes(rng.getrandbits(8) for _ in range(1024 * 1024 * 3)))
    b64 = preview_b64(noisy)
    assert b64 is not None and len(b64) <= 64 * 1024


def test_indexed_names_numeric_order() -> None:
    files = {"ref_10": "", "ref_2": "", "ref_0": "", "reference_1": "", "ref_x": ""}
    assert indexed_names(files, "ref_") == ["ref_0", "ref_2", "ref_10"]


def test_reference_guide_preserves_all_subjects_and_flattens_transparency() -> None:
    a = Image.new("RGB", (64, 128), "black")
    b = Image.new("RGBA", (128, 64), (0, 0, 0, 0))
    sheet = imaging.reference_guide([a, b])
    assert sheet.size == (768, 384)
    assert sheet.getpixel((192, 192)) == (0, 0, 0)
    assert sheet.getpixel((576, 192)) == (255, 255, 255)
    assert sheet.getpixel((0, 192)) == (255, 255, 255)  # aspect ratio is retained
