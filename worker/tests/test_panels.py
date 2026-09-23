"""Panel detection and reading order."""

from __future__ import annotations

import pytest
from PIL import Image, ImageDraw

from mangaveyra_worker.vision import panels as P
from tests.conftest import three_panel_page


def _lefts(result: list[dict]) -> list[int]:
    return [min(pt[0] for pt in p["points"]) for p in result]


def _tops(result: list[dict]) -> list[int]:
    return [min(pt[1] for pt in p["points"]) for p in result]


@pytest.mark.parametrize("method", ["gutter", "contour"])
def test_three_panel_page_rtl(method: str) -> None:
    if method == "contour" and not P.HAVE_CV2:
        pytest.skip("OpenCV not installed")
    page = three_panel_page()
    result = P.detect_panels(page, "rtl", method)
    assert len(result) == 3
    assert [p["order"] for p in result] == [1, 2, 3]
    tops, lefts = _tops(result), _lefts(result)
    assert tops[0] < tops[1] == tops[2] or abs(tops[1] - tops[2]) < 5  # wide top panel first
    assert lefts[1] > lefts[2]  # then right-hand panel (manga)
    # Polygons hug the drawn frames (5 px border at 40 px margins).
    top = result[0]["points"]
    assert abs(min(p[0] for p in top) - 40) <= 3 and abs(min(p[1] for p in top) - 40) <= 3


def test_three_panel_page_ltr_and_vertical() -> None:
    page = three_panel_page()
    ltr = P.detect_panels(page, "ltr", "gutter")
    assert _lefts(ltr)[1] < _lefts(ltr)[2]
    vertical = P.detect_panels(page, "vertical", "gutter")
    assert _tops(vertical)[0] < _tops(vertical)[1]


def test_pure_pillow_path_when_opencv_missing(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(P, "HAVE_CV2", False)
    assert len(P.detect_panels(three_panel_page(), "rtl")) == 3
    assert P.detect_bubbles(three_panel_page()) == []


def test_large_page_is_scaled_back_to_original_coordinates() -> None:
    page = three_panel_page(2400, 3390)
    result = P.detect_panels(page, "rtl", "gutter")
    assert len(result) == 3
    assert max(pt[0] for p in result for pt in p["points"]) > 2200


def test_blank_page_is_one_panel() -> None:
    result = P.detect_panels(Image.new("RGB", (300, 400), "white"), "rtl")
    assert result == [{"points": [[0, 0], [300, 0], [300, 400], [0, 400]], "order": 1}]


def test_tall_right_panel_reads_first_in_rtl() -> None:
    img = Image.new("RGB", (800, 1000), "white")
    d = ImageDraw.Draw(img)
    d.rectangle((420, 40, 760, 960), outline="black", width=5)  # tall right
    d.rectangle((40, 40, 380, 480), outline="black", width=5)  # left top
    d.rectangle((40, 520, 380, 960), outline="black", width=5)  # left bottom
    result = P.detect_panels(img, "rtl", "gutter")
    assert _lefts(result) == [420, 40, 40]
    assert _tops(result)[1] < _tops(result)[2]


@pytest.mark.skipif(not P.HAVE_CV2, reason="OpenCV not installed")
def test_slanted_gutter_needs_contours() -> None:
    img = Image.new("RGB", (800, 1130), "white")
    d = ImageDraw.Draw(img)
    d.polygon([(40, 40), (760, 40), (760, 400), (40, 480)], outline="black", width=5)
    d.polygon([(40, 520), (760, 440), (760, 1090), (40, 1090)], outline="black", width=5)
    assert len(P.detect_panels(img, "rtl", "gutter")) == 1  # no straight gutter crosses the page
    result = P.detect_panels(img, "rtl", "auto")
    assert len(result) == 2
    assert len(result[0]["points"]) == 4 and result[0]["points"][0] == [40, 40]


@pytest.mark.skipif(not P.HAVE_CV2, reason="OpenCV not installed")
def test_detects_speech_bubble() -> None:
    img = three_panel_page()
    d = ImageDraw.Draw(img)
    d.rectangle((60, 60, 740, 470), fill=(150, 150, 150), outline="black", width=5)  # toned panel
    d.ellipse((400, 90, 640, 230), fill="white", outline="black", width=3)
    for i in range(4):
        d.line((450, 120 + i * 25, 590, 120 + i * 25), fill="black", width=4)  # "text"
    bubbles = P.detect_bubbles(img)
    assert len(bubbles) >= 1
    xs = [pt[0] for pt in bubbles[0]["points"]]
    assert 390 <= min(xs) and max(xs) <= 650


def test_reading_order_helper() -> None:
    boxes = [[(0, 0), (10, 0), (10, 10), (0, 10)], [(20, 0), (30, 0), (30, 10), (20, 10)]]
    assert P.reading_order(boxes, "rtl") == [1, 0]
    assert P.reading_order(boxes, "ltr") == [0, 1]
    with pytest.raises(ValueError):
        P.detect_panels(Image.new("RGB", (10, 10)), "diagonal")


def test_normalise_polygon() -> None:
    assert P.normalise_polygon([(10, 0), (10, 10), (0, 10), (0, 0)]) == [[0, 0], [10, 0], [10, 10], [0, 10]]
    assert P.normalise_polygon([(0, 0), (0, 10), (10, 10), (10, 0)]) == [[0, 0], [10, 0], [10, 10], [0, 10]]
