"""Pure-Pillow image helpers: labelled screentone placeholders, OpenPose skeleton rendering, tone finishes.

The placeholder look follows the design system's ``tone()`` / ``ph()`` helpers: a Paper-theme surface
(``#DCE3F3``) with a 7 px ink dot grid, and a mono label chip (``#F7F9FF`` on ``#3E4A66``) in the bottom-left
corner — so mock output is instantly recognisable as a placeholder and never mistaken for real artwork.
"""

from __future__ import annotations

import colorsys
import hashlib
import json
import math
import random
import struct
import textwrap
from functools import lru_cache
from typing import Any, Iterable, Mapping, Sequence

from PIL import Image, ImageChops, ImageDraw, ImageFilter, ImageFont, ImageOps

INK = (7, 10, 18)
PAPER = (247, 249, 255)
TONE_BG = (220, 227, 243)
LABEL_TEXT = (62, 74, 102)
ACCENT = (61, 139, 255)

#: Finishing styles accepted by image jobs (``style`` / ``finish`` inputs).
STYLES = ("bw_screentone", "flat_grey", "colour", "screentone")


def reference_guide(images: Sequence[Image.Image]) -> Image.Image:
    """One bounded IP-Adapter guide retaining every supplied reference.

    Multiple source images are laid out without distortion, labels or new art.
    This avoids silently ignoring reference 2 onward on a single-adapter model.
    """
    if not images or len(images) > 8:
        raise ValueError("reference guide needs 1–8 images")
    if len(images) == 1:
        return images[0]
    cols = math.ceil(math.sqrt(len(images)))
    rows = math.ceil(len(images) / cols)
    edge = 384
    sheet = Image.new("RGB", (cols * edge, rows * edge), "white")
    for i, image in enumerate(images):
        rgba = image.convert("RGBA")
        flat = Image.new("RGBA", rgba.size, "white")
        flat.alpha_composite(rgba)
        thumb = ImageOps.contain(flat.convert("RGB"), (edge, edge))
        sheet.paste(thumb, ((i % cols) * edge + (edge - thumb.width) // 2,
                            (i // cols) * edge + (edge - thumb.height) // 2))
    return sheet


@lru_cache(maxsize=16)
def font(size: int) -> ImageFont.FreeTypeFont | ImageFont.ImageFont:
    """Pillow's bundled font at ``size`` px (a bitmap fallback when FreeType is unavailable)."""
    try:
        return ImageFont.load_default(size=size)
    except TypeError:  # pragma: no cover - Pillow < 10.1
        return ImageFont.load_default()


def stable_seed(*parts: Any) -> int:
    """A 32-bit seed derived from arbitrary JSON-able parts (stable across runs and platforms)."""
    blob = json.dumps(parts, sort_keys=True, default=str, ensure_ascii=False).encode()
    return int.from_bytes(hashlib.sha256(blob).digest()[:4], "big")


def _style_palette(style: str, rng: random.Random) -> tuple[tuple[int, int, int], tuple[int, int, int]]:
    """Background and dot colours for a style."""
    if style == "colour":
        hue = rng.random()
        bg = colorsys.hsv_to_rgb(hue, 0.18, 0.96)
        dot = colorsys.hsv_to_rgb((hue + 0.55) % 1.0, 0.65, 0.35)
        return tuple(int(c * 255) for c in bg), tuple(int(c * 255) for c in dot)  # type: ignore[return-value]
    if style == "flat_grey":
        return (214, 214, 214), (90, 90, 90)
    return TONE_BG, INK


def halftone(
    size: tuple[int, int],
    *,
    spacing: int,
    angle: float,
    density: float,
    bg: tuple[int, int, int],
    dot: tuple[int, int, int],
    focus: tuple[float, float] | None = None,
) -> Image.Image:
    """A dot-grid screentone whose dot radius follows a linear gradient (``angle``) or a radial ``focus``."""
    w, h = size
    img = Image.new("RGB", size, bg)
    draw = ImageDraw.Draw(img)
    ca, sa = math.cos(angle), math.sin(angle)
    diag = math.hypot(w, h) or 1.0
    r_max = spacing * 0.36
    for y in range(spacing // 2, h + spacing, spacing):
        for x in range(spacing // 2, w + spacing, spacing):
            if focus is not None:
                t = 1.0 - min(1.0, math.hypot(x - focus[0] * w, y - focus[1] * h) / (0.75 * diag))
            else:
                t = ((x - w / 2) * ca + (y - h / 2) * sa) / diag + 0.5
            r = r_max * (0.22 + 0.78 * max(0.0, min(1.0, density * t)))
            if r >= 0.45:
                draw.ellipse((x - r, y - r, x + r, y + r), fill=dot)
    return img


def speed_lines(img: Image.Image, rng: random.Random, colour: tuple[int, int, int], count: int = 48) -> None:
    """Draw radial manga speed lines towards a random focal point (in place)."""
    w, h = img.size
    draw = ImageDraw.Draw(img)
    fx, fy = w * rng.uniform(0.35, 0.65), h * rng.uniform(0.35, 0.65)
    reach = math.hypot(w, h)
    inner = min(w, h) * rng.uniform(0.22, 0.34)
    for _ in range(count):
        a = rng.uniform(0, 2 * math.pi)
        r0 = inner * rng.uniform(1.0, 1.6)
        width = max(1, int(min(w, h) / 220 * rng.uniform(0.6, 2.2)))
        draw.line(
            (
                fx + math.cos(a) * r0,
                fy + math.sin(a) * r0,
                fx + math.cos(a) * reach,
                fy + math.sin(a) * reach,
            ),
            fill=colour,
            width=width,
        )


def figure(
    draw: ImageDraw.ImageDraw,
    cx: float,
    base: float,
    height: float,
    *,
    fill: Any,
    outline: Any,
    view: str = "front",
) -> None:
    """A neutral mannequin silhouette (head + torso + legs) used as a placeholder subject."""
    head = height * 0.13
    stroke = max(2, int(height / 90))
    body_w = height * (0.22 if view == "front" else 0.14)
    top = base - height
    draw.ellipse(
        (cx - head * 0.8, top, cx + head * 0.8, top + head * 2), fill=fill, outline=outline, width=stroke
    )
    torso_top = top + head * 2.1
    torso_bot = top + height * 0.58
    draw.rounded_rectangle(
        (cx - body_w / 2, torso_top, cx + body_w / 2, torso_bot),
        radius=body_w * 0.25,
        fill=fill,
        outline=outline,
        width=stroke,
    )
    leg_w = body_w * 0.36
    for dx in (-body_w / 4, body_w / 4) if view == "front" else (0.0,):
        draw.rounded_rectangle(
            (cx + dx - leg_w / 2, torso_bot - stroke, cx + dx + leg_w / 2, base),
            radius=leg_w * 0.3,
            fill=fill,
            outline=outline,
            width=stroke,
        )


def label_chip(img: Image.Image, text: str, *, corner: str = "bl", scale: float = 1.0) -> None:
    """Draw a mono label chip (``#F7F9FF`` background, ``#3E4A66`` text) in a corner (in place)."""
    w, h = img.size
    size = max(10, int(min(w, h) / 48 * scale))
    f = font(size)
    draw = ImageDraw.Draw(img)
    pad_x, pad_y, margin = int(size * 0.5), int(size * 0.3), int(size * 0.8)
    box = draw.textbbox((0, 0), text, font=f)
    tw, th = box[2] - box[0], box[3] - box[1]
    if corner == "bl":
        x0, y0 = margin, h - margin - th - 2 * pad_y
    else:
        x0, y0 = margin, margin
    draw.rectangle((x0, y0, x0 + tw + 2 * pad_x, y0 + th + 2 * pad_y), fill=PAPER)
    draw.text((x0 + pad_x - box[0], y0 + pad_y - box[1]), text, font=f, fill=LABEL_TEXT)


def wrap_prompt(prompt: str, width_px: int, size: int, max_lines: int = 3) -> str:
    """Wrap and truncate a prompt for a chip that is ``width_px`` wide."""
    chars = max(12, int(width_px / (size * 0.55)))
    lines = textwrap.wrap(" ".join(prompt.split()), width=chars)[:max_lines]
    if len(lines) == max_lines:
        lines[-1] = lines[-1][: max(0, chars - 1)] + "…"
    return "\n".join(lines)


def placeholder(
    width: int,
    height: int,
    *,
    seed: int,
    job_type: str,
    prompt: str = "",
    style: str = "bw_screentone",
    variation: int = 1,
    total: int = 1,
    subject: str = "figure",
) -> Image.Image:
    """Render a deterministic labelled screentone placeholder.

    Equal arguments give byte-identical images; different seeds or variations change the tone angle,
    density, focal point, speed lines and subject placement so variations are visibly different.
    """
    rng = random.Random(stable_seed(seed, job_type, prompt, style, variation, width, height))
    bg, dot = _style_palette(style, rng)
    spacing = max(6, round(min(width, height) / 110))
    img = halftone(
        (width, height),
        spacing=spacing,
        angle=rng.uniform(0, 2 * math.pi),
        density=rng.uniform(0.55, 1.0),
        bg=bg,
        dot=dot,
        focus=(rng.uniform(0.2, 0.8), rng.uniform(0.2, 0.8)) if rng.random() < 0.5 else None,
    )
    if rng.random() < 0.6:
        speed_lines(img, rng, dot if style != "colour" else INK, count=rng.randint(28, 64))
    draw = ImageDraw.Draw(img)
    fill = PAPER if style != "colour" else tuple(min(255, c + 25) for c in bg)
    if subject == "sheet":
        stroke = max(2, int(height / 90))
        for i, view in enumerate(("front", "side")):
            figure(
                draw, width * (i + 0.5) / 3, height * 0.92, height * 0.78, fill=fill, outline=INK, view=view
            )
        # Face close-up: a large head over shoulders in the third column.
        cx, cy, r = width * 2.5 / 3, height * 0.45, min(width / 8, height * 0.22)
        draw.rounded_rectangle(
            (cx - r * 1.6, cy + r * 1.05, cx + r * 1.6, height * 0.92),
            radius=r * 0.5,
            fill=fill,
            outline=INK,
            width=stroke,
        )
        draw.ellipse((cx - r, cy - r * 1.2, cx + r, cy + r * 1.2), fill=fill, outline=INK, width=stroke)
        for i, name in enumerate(("FRONT", "SIDE", "FACE")):
            label_chip_at(img, width * i / 3 + width * 0.03, height * 0.08, name)
    elif subject == "figure":
        n = rng.randint(1, 2)
        for i in range(n):
            cx = (
                width * rng.uniform(0.25, 0.75)
                if n == 1
                else width * (0.3 + 0.4 * i) + rng.uniform(-0.05, 0.05) * width
            )
            hgt = height * rng.uniform(0.45, 0.8)
            figure(
                draw,
                cx,
                height * rng.uniform(0.88, 1.02),
                hgt,
                fill=fill,
                outline=INK,
                view=rng.choice(("front", "side")),
            )
    chip = f"PLACEHOLDER · {job_type.upper()} · V{variation}/{total} · SEED {seed}"
    label_chip(img, chip, corner="bl")
    if prompt:
        size = max(10, int(min(width, height) / 48))
        label_chip(img, wrap_prompt(prompt, int(width * 0.8), size), corner="tl", scale=0.9)
    if style in ("bw_screentone", "screentone", "flat_grey"):
        img = ImageOps.grayscale(img).convert("RGB")
    return img


def label_chip_at(img: Image.Image, x: float, y: float, text: str) -> None:
    """A small chip at an absolute position (used for sheet view names)."""
    size = max(10, int(min(img.size) / 40))
    f = font(size)
    draw = ImageDraw.Draw(img)
    box = draw.textbbox((0, 0), text, font=f)
    pad = int(size * 0.35)
    draw.rectangle((x, y, x + box[2] - box[0] + 2 * pad, y + box[3] - box[1] + 2 * pad), fill=PAPER)
    draw.text((x + pad - box[0], y + pad - box[1]), text, font=f, fill=LABEL_TEXT)


def tone_overlay(base: Image.Image, *, seed: int, style: str, strength: float) -> Image.Image:
    """Blend a screentone layer over ``base`` (used by mock edit/inpaint): ``strength`` 0 = unchanged, 1 = tone only."""
    rng = random.Random(seed)
    bg, dot = _style_palette(style, rng)
    spacing = max(6, round(min(base.size) / 110))
    layer = halftone(
        base.size, spacing=spacing, angle=rng.uniform(0, 6.28), density=rng.uniform(0.6, 1.0), bg=bg, dot=dot
    )
    if style in ("bw_screentone", "screentone", "flat_grey"):
        layer = ImageOps.grayscale(layer).convert("RGB")
    return Image.blend(base.convert("RGB"), layer, max(0.0, min(1.0, strength)))


def ink_sketch(sketch: Image.Image, *, strength: float, finish: str, seed: int) -> Image.Image:
    """Turn a rough sketch into clean ink lines with a tone finish (pure Pillow).

    ``strength`` 1 keeps the sketch lines exactly (thin clean-up); 0 lets the lines loosen (heavier, smoothed).
    """
    grey = ImageOps.autocontrast(ImageOps.grayscale(sketch), cutoff=1)
    blur = 1.6 * (1.0 - strength) + 0.4
    smooth = grey.filter(ImageFilter.GaussianBlur(blur))
    lines = smooth.point(lambda v: 0 if v < 150 else 255, mode="L")
    if strength < 0.7:
        lines = lines.filter(ImageFilter.MinFilter(3))  # thicken loose strokes
    lines_rgb = Image.merge("RGB", (lines, lines, lines))
    style = {"screentone": "bw_screentone", "flat_grey": "flat_grey", "colour": "colour"}.get(
        finish, "bw_screentone"
    )
    toned = tone_overlay(Image.new("RGB", sketch.size, PAPER), seed=seed, style=style, strength=0.55)
    # Ink lines on top of the tone: multiply keeps black lines black and the tone in the whitespace.
    return ImageChops.multiply(toned, lines_rgb)


# ---------------------------------------------------------------------- OpenPose

#: COCO-18 limb pairs (0-based) and the conventional OpenPose colours, as used by ControlNet OpenPose models.
OPENPOSE_LIMBS = [
    (1, 2),
    (1, 5),
    (2, 3),
    (3, 4),
    (5, 6),
    (6, 7),
    (1, 8),
    (8, 9),
    (9, 10),
    (1, 11),
    (11, 12),
    (12, 13),
    (1, 0),
    (0, 14),
    (14, 16),
    (0, 15),
    (15, 17),
]
OPENPOSE_COLOURS = [
    (255, 0, 0),
    (255, 85, 0),
    (255, 170, 0),
    (255, 255, 0),
    (170, 255, 0),
    (85, 255, 0),
    (0, 255, 0),
    (0, 255, 85),
    (0, 255, 170),
    (0, 255, 255),
    (0, 170, 255),
    (0, 85, 255),
    (0, 0, 255),
    (85, 0, 255),
    (170, 0, 255),
    (255, 0, 255),
    (255, 0, 170),
    (255, 0, 85),
]


def _people(pose: Mapping[str, Any]) -> Iterable[list[tuple[float, float, float]]]:
    """Yield keypoint lists ``[(x, y, confidence)] * 18`` from OpenPose JSON or ``{"keypoints": [[x, y], …]}``."""
    if "people" in pose:
        for person in pose.get("people") or []:
            flat = person.get("pose_keypoints_2d") or []
            yield [(flat[i], flat[i + 1], flat[i + 2]) for i in range(0, len(flat) - 2, 3)]
    elif "keypoints" in pose:
        pts = pose["keypoints"]
        if pts and isinstance(pts[0], (list, tuple)) and pts and isinstance(pts[0][0], (list, tuple)):
            for person in pts:
                yield [(p[0], p[1], p[2] if len(p) > 2 else 1.0) for p in person]
        else:
            yield [(p[0], p[1], p[2] if len(p) > 2 else 1.0) for p in pts]


def render_openpose(pose: Mapping[str, Any], width: int, height: int) -> Image.Image:
    """Render OpenPose JSON as the black-background skeleton image ControlNet OpenPose models expect.

    Coordinates may be absolute pixels in ``canvas_width × canvas_height`` or normalised 0–1.
    """
    img = Image.new("RGB", (width, height), (0, 0, 0))
    overlay = Image.new("RGB", (width, height), (0, 0, 0))
    draw = ImageDraw.Draw(overlay)
    cw = float(pose.get("canvas_width") or 0)
    ch = float(pose.get("canvas_height") or 0)
    stick = max(2, int(min(width, height) / 128))
    for person in _people(pose):
        xs = [p[0] for p in person if p[2] > 0]
        normalised = bool(xs) and max(xs) <= 1.0 and max(p[1] for p in person if p[2] > 0) <= 1.0
        sx = width if normalised else (width / cw if cw else 1.0)
        sy = height if normalised else (height / ch if ch else 1.0)
        pts = [(p[0] * sx, p[1] * sy, p[2]) for p in person]
        for i, (a, b) in enumerate(OPENPOSE_LIMBS):
            if a < len(pts) and b < len(pts) and pts[a][2] > 0 and pts[b][2] > 0:
                (x0, y0, _), (x1, y1, _) = pts[a], pts[b]
                draw.line(
                    (x0, y0, x1, y1), fill=tuple(int(c * 0.6) for c in OPENPOSE_COLOURS[i]), width=stick * 2
                )
        for i, (x, y, c) in enumerate(pts[:18]):
            if c > 0:
                r = stick * 1.5
                draw.ellipse((x - r, y - r, x + r, y + r), fill=OPENPOSE_COLOURS[i])
    img.paste(overlay)
    return img


# ---------------------------------------------------------------------- safetensors


def write_safetensors(
    tensors: Mapping[str, tuple[str, Sequence[int], bytes]], metadata: Mapping[str, str]
) -> bytes:
    """Serialise raw tensors to the safetensors format without numpy/torch.

    ``tensors`` maps name → ``(dtype, shape, raw_little_endian_bytes)``. Used for the mock LoRA artifact and in
    tests; real LoRAs are written by ``diffusers``/``safetensors``.
    """
    header: dict[str, Any] = {"__metadata__": dict(metadata)}
    offset = 0
    blobs = []
    for name, (dtype, shape, raw) in tensors.items():
        header[name] = {"dtype": dtype, "shape": list(shape), "data_offsets": [offset, offset + len(raw)]}
        offset += len(raw)
        blobs.append(raw)
    head = json.dumps(header, separators=(",", ":")).encode()
    head += b" " * ((8 - len(head) % 8) % 8)
    return struct.pack("<Q", len(head)) + head + b"".join(blobs)


def read_safetensors_metadata(data: bytes) -> dict[str, Any]:
    """Return the JSON header of a safetensors blob (used by tests and the LoRA cache)."""
    (n,) = struct.unpack("<Q", data[:8])
    return json.loads(data[8 : 8 + n])
