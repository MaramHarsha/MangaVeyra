"""Identity scoring, OCR engine selection and the vision backend."""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable

import pytest
from PIL import Image, ImageDraw

from mangaveyra_worker.config import Config
from mangaveyra_worker.vision.embed import StructuralEmbedder, centroid, cosine, score_identity
from mangaveyra_worker.vision.ocr import HttpEngine, NullEngine, make_ocr_engine
from tests.conftest import png, three_panel_page

np = pytest.importorskip("numpy")


def character(colour: tuple[int, int, int], offset: int = 0, seed: int = 0) -> Image.Image:
    img = Image.new("RGB", (128, 160), "white")
    d = ImageDraw.Draw(img)
    d.ellipse((34 + offset, 10, 94 + offset, 70), fill=colour, outline="black", width=3)
    d.rectangle((44 + offset, 72, 84 + offset, 140), fill=colour, outline="black", width=3)
    d.line((20, 150 - seed, 110, 150 - seed), fill="black", width=2)
    return img


def test_structural_similarity_ranks_near_duplicates_higher() -> None:
    refs = [character((200, 60, 60)), character((200, 60, 60), offset=3)]
    same = character((200, 60, 60), offset=1, seed=4)
    other = Image.new("RGB", (128, 160), "white")
    ImageDraw.Draw(other).polygon([(10, 150), (64, 5), (120, 150)], fill=(20, 20, 180))
    scores, method = score_identity(refs, [same, other], "structural")
    assert method == "structural"
    assert 0.0 <= scores[1] < scores[0] <= 1.0
    assert scores[0] > 0.8


def test_identical_image_scores_one() -> None:
    img = character((10, 120, 10))
    scores, _ = score_identity([img], [img], "structural")
    assert scores[0] == pytest.approx(1.0, abs=1e-4)


def test_vector_helpers() -> None:
    assert cosine([1, 0], [0, 1]) == 0
    assert centroid([[2, 0], [0, 2]]) == pytest.approx([2**-0.5, 2**-0.5])
    assert len(StructuralEmbedder().embed([character((0, 0, 0))])[0]) > 100


def test_ocr_engine_selection() -> None:
    assert make_ocr_engine("none").name == "none"  # type: ignore[union-attr]
    assert isinstance(NullEngine().recognise(Image.new("RGB", (4, 4)), "en"), list)
    with pytest.raises(ValueError):
        make_ocr_engine("http", None)


def test_http_ocr_engine() -> None:
    seen: dict[str, Any] = {}

    class H(BaseHTTPRequestHandler):
        def log_message(self, *a: Any) -> None:
            pass

        def do_POST(self) -> None:  # noqa: N802
            seen["ctype"] = self.headers["Content-Type"]
            self.rfile.read(int(self.headers["Content-Length"]))
            body = json.dumps({"lines": [{"text": "ZAAAN", "box": [1, 2, 30, 40]}]}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    try:
        lines = HttpEngine(f"http://127.0.0.1:{srv.server_address[1]}/ocr").recognise(
            Image.new("RGB", (50, 50)), "ja"
        )
    finally:
        srv.shutdown()
    assert lines == [{"text": "ZAAAN", "box": [1, 2, 30, 40]}]
    assert seen["ctype"].startswith("multipart/form-data")


def test_vision_backend(run_job: Callable[..., Any]) -> None:
    pytest.importorskip("cv2")
    from mangaveyra_worker.backends.vision import VisionBackend

    backend = VisionBackend(Config(ocr_engine="none", embedder="structural"), [])
    assert backend.job_types() == ["detect.panels", "character.score"]
    result, _ = run_job(backend, "detect.panels", {"reading": "ltr"}, {"page": png(three_panel_page())})
    assert len(result.output["panels"]) == 3
    files = {
        "reference_0": png(character((200, 60, 60))),
        "candidate_0": png(character((200, 60, 60), offset=2)),
        "candidate_1": png(Image.new("RGB", (128, 160), (5, 5, 90))),
    }
    result, _ = run_job(backend, "character.score", {"threshold": 0.8}, files)
    assert result.output["method"] == "structural"
    assert result.output["scores"][0] > result.output["scores"][1]
