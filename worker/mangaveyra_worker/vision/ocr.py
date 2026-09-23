"""Pluggable OCR for the ``ocr`` job type (translation and import of existing comics).

Engines (select with ``MV_OCR_ENGINE``; ``auto`` picks the first available in this order):

* ``manga-ocr`` — `manga-ocr <https://github.com/kha-white/manga-ocr>`_ (Apache-2.0), excellent for Japanese
  manga text including vertical lines. It recognises one text block at a time, so blocks are located first with the
  speech-bubble detector (OpenCV) and each crop is recognised separately. ``pip install manga-ocr``.
* ``tesseract`` — Tesseract through ``pytesseract`` (Apache-2.0) for Latin-script comics; needs the ``tesseract``
  binary and language packs installed on the system.
* ``http`` — POSTs the image to ``MV_OCR_URL`` as multipart ``image`` (+ ``lang``) and expects
  ``{"lines": [{"text", "box": [x, y, w, h]}]}``. Use it to run GPL tools such as *manga-image-translator* as a
  separate service: MangaVeyra never vendors or links GPL code, it only talks to it over HTTP.
* ``none`` — returns no lines (the job still succeeds; bubbles stay empty for a human to fill).
"""

from __future__ import annotations

import io
import logging
import threading
from typing import Any, Protocol

from PIL import Image

log = logging.getLogger(__name__)


class OcrEngine(Protocol):
    """Recognises text lines in an image."""

    name: str

    def recognise(self, img: Image.Image, lang: str) -> list[dict[str, Any]]:
        """Return ``[{"text": str, "box": [x, y, w, h]}]`` in image pixels."""


def _bubble_boxes(img: Image.Image) -> list[list[int]]:
    from mangaveyra_worker.vision.panels import detect_bubbles

    boxes = []
    for b in detect_bubbles(img):
        xs = [p[0] for p in b["points"]]
        ys = [p[1] for p in b["points"]]
        boxes.append([min(xs), min(ys), max(xs) - min(xs), max(ys) - min(ys)])
    return boxes


class MangaOcrEngine:
    """manga-ocr on each detected text block (whole image when no block is found)."""

    name = "manga-ocr"
    _lock = threading.Lock()
    _model: Any = None

    def __init__(self) -> None:
        import manga_ocr  # noqa: F401

    def _load(self) -> Any:
        from manga_ocr import MangaOcr

        with self._lock:
            if MangaOcrEngine._model is None:
                MangaOcrEngine._model = MangaOcr()
        return MangaOcrEngine._model

    def recognise(self, img: Image.Image, lang: str) -> list[dict[str, Any]]:
        """Recognise text in every detected bubble."""
        model = self._load()
        boxes = _bubble_boxes(img) or [[0, 0, img.width, img.height]]
        lines = []
        for x, y, w, h in boxes:
            text = str(model(img.crop((x, y, x + w, y + h)))).strip()
            if text:
                lines.append({"text": text, "box": [x, y, w, h]})
        return lines


#: ISO 639-1 → Tesseract language codes for the languages MangaVeyra ships or commonly translates.
TESSERACT_LANGS = {
    "en": "eng",
    "ja": "jpn",
    "ko": "kor",
    "zh": "chi_sim",
    "fr": "fra",
    "de": "deu",
    "es": "spa",
    "pt": "por",
    "it": "ita",
    "ru": "rus",
    "ar": "ara",
    "he": "heb",
    "id": "ind",
    "th": "tha",
    "vi": "vie",
}


class TesseractEngine:
    """Tesseract via pytesseract, grouped into lines."""

    name = "tesseract"

    def __init__(self) -> None:
        import pytesseract

        pytesseract.get_tesseract_version()  # raises when the binary is missing

    def recognise(self, img: Image.Image, lang: str) -> list[dict[str, Any]]:
        """Recognise text lines."""
        import pytesseract

        code = TESSERACT_LANGS.get(lang.split("-")[0].lower(), "eng")
        data = pytesseract.image_to_data(img, lang=code, output_type=pytesseract.Output.DICT)
        groups: dict[tuple[int, int, int], dict[str, Any]] = {}
        for i, word in enumerate(data["text"]):
            if not word.strip() or float(data["conf"][i]) < 0:
                continue
            key = (data["block_num"][i], data["par_num"][i], data["line_num"][i])
            x, y, w, h = data["left"][i], data["top"][i], data["width"][i], data["height"][i]
            g = groups.setdefault(key, {"words": [], "x0": x, "y0": y, "x1": x + w, "y1": y + h})
            g["words"].append(word)
            g["x0"], g["y0"] = min(g["x0"], x), min(g["y0"], y)
            g["x1"], g["y1"] = max(g["x1"], x + w), max(g["y1"], y + h)
        return [
            {"text": " ".join(g["words"]), "box": [g["x0"], g["y0"], g["x1"] - g["x0"], g["y1"] - g["y0"]]}
            for g in groups.values()
        ]


class HttpEngine:
    """Delegates OCR to an external HTTP service."""

    name = "http"

    def __init__(self, url: str, timeout: float = 120.0) -> None:
        if not url:
            raise ValueError("MV_OCR_URL is required for the http OCR engine")
        self.url, self.timeout = url, timeout

    def recognise(self, img: Image.Image, lang: str) -> list[dict[str, Any]]:
        """POST the image and parse ``{"lines": [...]}``."""
        import httpx

        buf = io.BytesIO()
        img.save(buf, format="PNG")
        resp = httpx.post(
            self.url,
            files={"image": ("page.png", buf.getvalue(), "image/png")},
            data={"lang": lang},
            timeout=self.timeout,
        )
        resp.raise_for_status()
        lines = []
        for line in resp.json().get("lines") or []:
            box = [int(v) for v in (line.get("box") or [0, 0, 0, 0])[:4]]
            lines.append({"text": str(line.get("text") or ""), "box": box})
        return lines


class NullEngine:
    """Returns no text."""

    name = "none"

    def recognise(self, img: Image.Image, lang: str) -> list[dict[str, Any]]:
        """Return nothing."""
        return []


def make_ocr_engine(preference: str = "auto", url: str | None = None) -> OcrEngine | None:
    """Build the configured engine. With ``auto``, returns ``None`` when no real engine is available."""
    # An admin-provided MV_OCR_URL is an explicit choice, so it goes first under auto.
    auto_order = ["http", "manga-ocr", "tesseract"] if url else ["manga-ocr", "tesseract"]
    order = auto_order if preference == "auto" else [preference]
    for name in order:
        try:
            if name == "manga-ocr":
                return MangaOcrEngine()
            if name == "tesseract":
                return TesseractEngine()
            if name == "http":
                if not url and preference == "auto":
                    continue
                return HttpEngine(url or "")
            if name == "none":
                return NullEngine()
        except Exception as exc:  # ImportError, missing binary, ...
            if preference != "auto":
                raise
            log.debug("OCR engine %s unavailable: %s", name, exc)
    return None
