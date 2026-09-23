"""Panel and speech-bubble detection on scanned or imported comic pages.

Two panel detectors are available:

* **Gutter scan (pure Pillow)** — a recursive XY-cut: a page is split along blank rows (gutters) and then blank
  columns, recursively, until no gutter crosses a region. Works on any machine and handles the usual rectangular
  manga/comic grids, including nested layouts.
* **Contour (OpenCV)** — external contours of the inked panel frames approximated to polygons, which also handles
  slanted gutters and non-rectangular panels. Used when ``opencv-python-headless`` is installed.

``detect_panels(method="auto")`` runs both (when OpenCV is present) and keeps the result that finds more panels
(ties go to the contour polygons, which are more precise). Reading order is computed by a recursive XY-cut over the
panel boxes: horizontal bands top to bottom, and within a band right-to-left for manga (``rtl``), left-to-right for
comics (``ltr``); ``vertical`` (webtoon) is top to bottom.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass
from typing import Any, Sequence

from PIL import Image, ImageOps

log = logging.getLogger(__name__)

#: Grey level below which a pixel counts as ink.
INK_THRESHOLD = 200
#: Largest side the detectors work at (coordinates are scaled back to the original image).
WORK_SIDE = 1200
#: Minimum panel area as a fraction of the page.
MIN_PANEL_FRACTION = 0.012
#: A row/column is a gutter when at most this fraction of its pixels is ink.
GUTTER_INK = 0.004

READINGS = ("rtl", "ltr", "vertical")

try:  # optional acceleration
    import cv2  # type: ignore[import-not-found]
    import numpy as np  # type: ignore[import-not-found]

    HAVE_CV2 = True
except ImportError:  # pragma: no cover - exercised when the vision extra is missing
    cv2 = None
    np = None
    HAVE_CV2 = False


@dataclass(frozen=True)
class Box:
    """Axis-aligned box ``(x0, y0, x1, y1)`` (exclusive right/bottom)."""

    x0: int
    y0: int
    x1: int
    y1: int

    @property
    def w(self) -> int:
        """Width."""
        return self.x1 - self.x0

    @property
    def h(self) -> int:
        """Height."""
        return self.y1 - self.y0

    @property
    def area(self) -> int:
        """Area in px²."""
        return max(0, self.w) * max(0, self.h)


def _work_image(img: Image.Image) -> tuple[Image.Image, float]:
    grey = ImageOps.grayscale(img)
    scale = 1.0
    side = max(grey.size)
    if side > WORK_SIDE:
        scale = WORK_SIDE / side
        grey = grey.resize(
            (max(1, round(grey.width * scale)), max(1, round(grey.height * scale))), Image.Resampling.BOX
        )
    return grey, scale


def _ink_mask(grey: Image.Image) -> Image.Image:
    return grey.point(lambda v: 255 if v < INK_THRESHOLD else 0, mode="L")


# ---------------------------------------------------------------------- gutter scan (pure Pillow)


def _profile(mask: Image.Image, box: Box, axis: str) -> list[float]:
    """Ink fraction per row (``axis="rows"``) or per column of ``box``, via a BOX-filter resize."""
    region = mask.crop((box.x0, box.y0, box.x1, box.y1))
    if axis == "rows":
        line = region.resize((1, region.height), Image.Resampling.BOX)
    else:
        line = region.resize((region.width, 1), Image.Resampling.BOX)
    return [v / 255.0 for v in line.tobytes()]


def _segments(profile: Sequence[float], min_gap: int) -> list[tuple[int, int]]:
    """Split ``[0, len)`` at gutters (runs of blank lines at least ``min_gap`` long)."""
    segs: list[tuple[int, int]] = []
    start: int | None = None
    blank_run = 0
    for i, v in enumerate(profile):
        if v <= GUTTER_INK:
            blank_run += 1
            if start is not None and blank_run >= min_gap:
                segs.append((start, i - blank_run + 1))
                start = None
        else:
            if start is None:
                start = i
            blank_run = 0
    if start is not None:
        end = len(profile)
        while end > start and profile[end - 1] <= GUTTER_INK:
            end -= 1
        segs.append((start, end))
    return [(a, b) for a, b in segs if b > a]


def _xycut(mask: Image.Image, box: Box, min_gap: int, depth: int = 0) -> list[Box]:
    bbox = mask.crop((box.x0, box.y0, box.x1, box.y1)).getbbox()
    if bbox is None:
        return []
    box = Box(box.x0 + bbox[0], box.y0 + bbox[1], box.x0 + bbox[2], box.y0 + bbox[3])
    if depth > 12:
        return [box]
    rows = _segments(_profile(mask, box, "rows"), min_gap)
    if len(rows) > 1:
        out: list[Box] = []
        for a, b in rows:
            out += _xycut(mask, Box(box.x0, box.y0 + a, box.x1, box.y0 + b), min_gap, depth + 1)
        return out
    cols = _segments(_profile(mask, box, "cols"), min_gap)
    if len(cols) > 1:
        out = []
        for a, b in cols:
            out += _xycut(mask, Box(box.x0 + a, box.y0, box.x0 + b, box.y1), min_gap, depth + 1)
        return out
    return [box]


def gutter_panels(img: Image.Image) -> list[list[tuple[int, int]]]:
    """Detect rectangular panels with the pure-Pillow recursive gutter scan. Returns polygons in image pixels."""
    grey, scale = _work_image(img)
    mask = _ink_mask(grey)
    min_gap = max(3, round(min(grey.size) * 0.008))
    page_area = grey.width * grey.height
    boxes = [
        b
        for b in _xycut(mask, Box(0, 0, grey.width, grey.height), min_gap)
        if b.area >= MIN_PANEL_FRACTION * page_area and min(b.w, b.h) >= 4 * min_gap
    ]
    inv = 1.0 / scale
    return [
        [
            (round(b.x0 * inv), round(b.y0 * inv)),
            (round(b.x1 * inv), round(b.y0 * inv)),
            (round(b.x1 * inv), round(b.y1 * inv)),
            (round(b.x0 * inv), round(b.y1 * inv)),
        ]
        for b in boxes
    ]


# ---------------------------------------------------------------------- contours (OpenCV)


def contour_panels(img: Image.Image) -> list[list[tuple[int, int]]]:
    """Detect panels as polygons from inked frame contours (requires OpenCV)."""
    if not HAVE_CV2:
        raise RuntimeError("OpenCV is not installed (pip install 'mangaveyra-worker[vision]')")
    grey, scale = _work_image(img)
    arr = np.asarray(grey, dtype=np.uint8)
    _, ink = cv2.threshold(arr, INK_THRESHOLD, 255, cv2.THRESH_BINARY_INV)
    ink = cv2.morphologyEx(ink, cv2.MORPH_CLOSE, np.ones((3, 3), np.uint8))
    contours, hierarchy = cv2.findContours(ink, cv2.RETR_TREE, cv2.CHAIN_APPROX_SIMPLE)
    if hierarchy is None:
        return []
    hier = hierarchy[0]
    page_area = float(arr.shape[0] * arr.shape[1])

    def children(idx: int) -> list[int]:
        out, c = [], hier[idx][2]
        while c != -1:
            out.append(c)
            c = hier[c][0]
        return out

    top = [i for i in range(len(contours)) if hier[i][3] == -1]
    # An outer page frame swallows every panel: descend frame → its hole → the panels inside.
    candidates: list[int] = []
    for i in top:
        if cv2.contourArea(contours[i]) > 0.85 * page_area:
            for hole in children(i):
                candidates.extend(children(hole))
        else:
            candidates.append(i)
    polys: list[list[tuple[int, int]]] = []
    inv = 1.0 / scale
    for i in candidates:
        c = contours[i]
        area = cv2.contourArea(c)
        if area < MIN_PANEL_FRACTION * page_area:
            continue
        hull = cv2.convexHull(c)
        # A frame's hull should be mostly filled by the contour; very concave blobs are art, not panels.
        if area / max(1.0, cv2.contourArea(hull)) < 0.7:
            continue
        approx = cv2.approxPolyDP(hull, 0.012 * cv2.arcLength(hull, True), True)
        pts = [(round(float(p[0][0]) * inv), round(float(p[0][1]) * inv)) for p in approx]
        if len(pts) >= 3:
            polys.append(pts)
    return polys


def detect_bubbles(img: Image.Image) -> list[dict[str, Any]]:
    """Find speech-bubble candidates: enclosed, roughly elliptical white regions that contain some ink (text).

    Returns ``[{"points": [[x, y], …], "text": "", "vertical": bool}]``. Requires OpenCV; returns ``[]`` without it.
    Text is filled in by an ``ocr`` job, not here.
    """
    if not HAVE_CV2:
        return []
    grey, scale = _work_image(img)
    arr = np.asarray(grey, dtype=np.uint8)
    _, white = cv2.threshold(arr, 235, 255, cv2.THRESH_BINARY)
    contours, _ = cv2.findContours(white, cv2.RETR_CCOMP, cv2.CHAIN_APPROX_SIMPLE)
    page_area = float(arr.shape[0] * arr.shape[1])
    inv = 1.0 / scale
    bubbles = []
    for c in contours:
        area = cv2.contourArea(c)
        if not 0.002 * page_area <= area <= 0.08 * page_area:
            continue
        x, y, w, h = cv2.boundingRect(c)
        extent = area / float(w * h)
        hull_area = cv2.contourArea(cv2.convexHull(c))
        if not 0.5 <= extent <= 0.95 or area / max(1.0, hull_area) < 0.85:
            continue
        mask = np.zeros_like(arr)
        cv2.drawContours(mask, [c], -1, 255, thickness=-1)
        inner = cv2.erode(mask, np.ones((5, 5), np.uint8))
        region = arr[inner > 0]
        if region.size == 0:
            continue
        ink_ratio = float((region < 128).mean())
        if not 0.01 <= ink_ratio <= 0.35:
            continue
        approx = cv2.approxPolyDP(c, 0.01 * cv2.arcLength(c, True), True)
        pts = [[round(float(p[0][0]) * inv), round(float(p[0][1]) * inv)] for p in approx][:32]
        bubbles.append({"points": pts, "text": "", "vertical": h > 1.3 * w})
    return bubbles


# ---------------------------------------------------------------------- reading order


def _bbox(poly: Sequence[Sequence[float]]) -> Box:
    xs = [p[0] for p in poly]
    ys = [p[1] for p in poly]
    return Box(int(min(xs)), int(min(ys)), int(max(xs)), int(max(ys)))


def _cut(items: list[tuple[int, Box]], axis: str, tol: float) -> list[list[tuple[int, Box]]] | None:
    """Split items into groups separated by a line no box straddles (by more than ``tol`` of its size)."""
    if axis == "y":
        spans = sorted((b.y0 + tol * b.h, b.y1 - tol * b.h, i, b) for i, b in items)
    else:
        spans = sorted((b.x0 + tol * b.w, b.x1 - tol * b.w, i, b) for i, b in items)
    groups: list[list[tuple[int, Box]]] = []
    current: list[tuple[int, Box]] = []
    reach = float("-inf")
    for lo, hi, i, b in spans:
        if current and lo >= reach:
            groups.append(current)
            current = []
        current.append((i, b))
        reach = max(reach, hi)
    groups.append(current)
    return groups if len(groups) > 1 else None


def reading_order(polys: Sequence[Sequence[Sequence[float]]], reading: str = "rtl") -> list[int]:
    """Return polygon indices in reading order for ``rtl`` (manga), ``ltr`` (comics) or ``vertical`` (webtoon)."""
    items = [(i, _bbox(p)) for i, p in enumerate(polys)]

    def order(group: list[tuple[int, Box]]) -> list[int]:
        if len(group) <= 1:
            return [i for i, _ in group]
        for tol in (0.0, 0.15):
            bands = _cut(group, "y", tol)
            if bands:
                return [i for band in bands for i in order(band)]
            if reading != "vertical":
                cols = _cut(group, "x", tol)
                if cols:
                    if reading == "rtl":
                        cols.reverse()
                    return [i for col in cols for i in order(col)]
        # Overlapping layout: fall back to row-major by top edge, then horizontal position.
        sign = -1 if reading == "rtl" else 1
        return [i for i, b in sorted(group, key=lambda t: (t[1].y0, sign * t[1].x0))]

    return order(items)


def normalise_polygon(poly: Sequence[Sequence[int]]) -> list[list[int]]:
    """Return the polygon clockwise (in image coordinates, y down) starting at the top-left-most vertex."""
    pts = [[int(p[0]), int(p[1])] for p in poly]
    signed = sum(
        pts[i][0] * pts[(i + 1) % len(pts)][1] - pts[(i + 1) % len(pts)][0] * pts[i][1]
        for i in range(len(pts))
    )
    if signed < 0:  # counter-clockwise on screen
        pts.reverse()
    start = min(range(len(pts)), key=lambda i: (pts[i][0] + pts[i][1], pts[i][1]))
    return pts[start:] + pts[:start]


def detect_panels(img: Image.Image, reading: str = "rtl", method: str = "auto") -> list[dict[str, Any]]:
    """Detect panels and return ``[{"points": [[x, y], …], "order": n}]`` sorted by reading order (1-based).

    ``method`` is ``auto``, ``gutter`` or ``contour``. A page with no detectable structure yields one panel
    covering the whole page, so an import always produces an editable page.
    """
    if reading not in READINGS:
        raise ValueError(f"reading must be one of {READINGS}")
    polys: list[list[tuple[int, int]]] = []
    if method in ("auto", "gutter"):
        polys = gutter_panels(img)
    if method == "contour" or (method == "auto" and HAVE_CV2):
        try:
            contour = contour_panels(img)
        except Exception as exc:  # pragma: no cover - defensive: never fail an import on the detector
            log.warning("contour panel detection failed: %s", exc)
            contour = []
        if method == "contour" or len(contour) >= len(polys):
            polys = contour or polys
    if not polys:
        w, h = img.size
        polys = [[(0, 0), (w, 0), (w, h), (0, h)]]
    idx = reading_order(polys, reading)
    return [{"points": normalise_polygon(polys[i]), "order": n + 1} for n, i in enumerate(idx)]
