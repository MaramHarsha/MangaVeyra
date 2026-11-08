"""Outfit model boundary/math checks; synthetic vectors prove no image quality."""
from __future__ import annotations

import copy
import hashlib
import json
from pathlib import Path
import importlib.util

import pytest

# Outfit scoring is an optional extra; a core install skips these checks.
np = pytest.importorskip("numpy")

from mangaveyra_worker.vision.outfit import FEATURES, PREPROCESSING, SCHEMA, framed_body, load_artifact, predict


def artifact():
    digest = lambda name: hashlib.sha256(name.encode()).hexdigest()
    heldout = [digest(f"heldout-{i}") for i in range(12)]
    model = {
        "schema": SCHEMA, "identity_gate": .9, "preprocessing": PREPROCESSING, "features": FEATURES, "threshold": .9,
        "weights": [2., 2.], "intercept": 0., "mean": [0., 0.], "scale": [1., 1.],
        "ccip_sha256": "a" * 64, "fashion_sha256": "b" * 64, "dataset_sha256": "c" * 64,
        "fashion_config_sha256": "d" * 64, "fashion_processor_sha256": "e" * 64,
        "frozen_fit_sha256": "f" * 64,
        "training_identities": ["Iori", "Toma", "Nao"],
        "heldout_identities": ["Yui", "Saki", "Ruka"],
        "training_image_sha256": [digest(f"train-{i}") for i in range(24)],
        "heldout_image_sha256": heldout,
    }
    pairs = []
    for reference in (0, 4, 8):
        for candidate in range(12):
            if candidate == reference:
                continue
            same = reference // 4 == candidate // 4
            features = [.9, .9] if same else [-.9, -.9]
            pairs.append({
                "reference_sha256": heldout[reference], "candidate_sha256": heldout[candidate],
                "same_outfit": same, "features": features, "score": predict([features], model)[0],
            })
    model["heldout_pairs"] = pairs
    model["independent_controls"] = [{
        "reference_sha256": digest("control-reference"), "candidate_sha256": digest(f"control-{i}"),
        "same_identity": i < 4, "identity_score": .96 if i < 4 else .6,
        "identity_method": "ccip-caformer24-v1:" + "a" * 64,
        "same_outfit": i < 2 or i >= 4, "features": [.9, .9] if i < 2 else [-.9, -.9],
        "score": predict([[.9, .9] if i < 2 else [-.9, -.9]], model)[0],
    } for i in range(6)]
    model["validation"] = {
        "split": "disjoint-identities", "positives": 9, "negatives": 24,
        "min_positive": min(p["score"] for p in pairs if p["same_outfit"]),
        "max_negative": max(p["score"] for p in pairs if not p["same_outfit"]),
    }
    return model


def test_outfit_artifact_reproduces_its_disjoint_validation(tmp_path):
    model = artifact()
    p = tmp_path / "model.json"
    p.write_text(json.dumps(model))
    loaded, digest = load_artifact(p)
    assert loaded == model
    assert digest == hashlib.sha256(p.read_bytes()).hexdigest()


@pytest.mark.parametrize("value", [[], {"validation": []}])
def test_malformed_artifact_objects_fail_closed(tmp_path, value):
    if isinstance(value, dict):
        value = {**artifact(), **value}
    path = tmp_path / "model.json"
    path.write_text(json.dumps(value))
    with pytest.raises(ValueError):
        load_artifact(path)


def test_outfit_framing_ignores_canvas_padding_and_keeps_the_whole_body_region():
    from PIL import Image, ImageChops, ImageDraw

    subject = Image.new("RGBA", (40, 160), (255, 0, 0, 0))
    draw = ImageDraw.Draw(subject)
    draw.rectangle((5, 5, 34, 154), fill="black")
    draw.rectangle((5, 135, 34, 154), fill="green")
    framed = Image.new("RGBA", (200, 240), (255, 0, 0, 0))
    framed.alpha_composite(subject, (80, 40))
    first, second = framed_body(subject), framed_body(framed)
    assert first.size == second.size == (512, 512)
    assert ImageChops.difference(first, second).getbbox() is None
    assert first.getpixel((256, 500)) == (0, 128, 0)
    assert first.getpixel((0, 256)) == (255, 255, 255)
    with pytest.raises(ValueError, match="no visible"):
        framed_body(Image.new("RGBA", (40, 160), (255, 0, 0, 0)))


def test_outfit_prediction_is_bounded_and_rejects_invalid_features():
    scores = predict([[.9, .9], [-.9, -.9]], artifact())
    assert 0 < scores[1] < scores[0] < 1
    with pytest.raises(ValueError):
        predict([[float("nan"), .9]], artifact())
    with pytest.raises(ValueError):
        predict([[.9]], artifact())


@pytest.mark.parametrize("fault", [
    "overlap", "low-positive", "high-negative", "missing-lineage", "zero-scale", "wrong-threshold",
    "image-overlap", "altered-parameters", "altered-proof", "foreign-proof-image", "missing-proof",
    "missing-controls", "failed-controls", "wrong-identity-accepted", "skip-positive-identity", "unsafe-gate",
])
def test_outfit_artifact_rejects_failed_or_unattributable_models(tmp_path, fault):
    model = copy.deepcopy(artifact())
    if fault == "overlap":
        model["heldout_identities"][0] = model["training_identities"][0]
    elif fault == "low-positive":
        model["validation"]["min_positive"] = .899
    elif fault == "high-negative":
        model["validation"]["max_negative"] = .9
    elif fault == "missing-lineage":
        model.pop("dataset_sha256")
    elif fault == "zero-scale":
        model["scale"][0] = 0
    elif fault == "wrong-threshold":
        model["threshold"] = .8
    elif fault == "image-overlap":
        model["training_image_sha256"][0] = model["heldout_image_sha256"][0]
    elif fault == "altered-parameters":
        model["weights"][0] = 1
    elif fault == "altered-proof":
        model["heldout_pairs"][0]["score"] = .99
    elif fault == "foreign-proof-image":
        model["heldout_pairs"][0]["candidate_sha256"] = model["training_image_sha256"][0]
    elif fault == "missing-proof":
        model.pop("heldout_pairs")
    elif fault == "missing-controls":
        model.pop("independent_controls")
    elif fault == "failed-controls":
        model["independent_controls"][0]["same_outfit"] = False
    elif fault == "wrong-identity-accepted":
        model["independent_controls"][-1]["identity_score"] = .96
    elif fault == "skip-positive-identity":
        model["independent_controls"][0]["identity_score"] = .8
    else:
        model["identity_gate"] = .8
    p = tmp_path / "model.json"
    p.write_text(json.dumps(model))
    with pytest.raises(ValueError):
        load_artifact(p)


def test_balanced_fit_uses_only_supplied_training_features():
    spec = importlib.util.spec_from_file_location("outfit_fit", Path(__file__).resolve().parents[1] / "tools" / "fit_outfit.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    x = np.asarray([[.9, .85], [.86, .9], [.88, .84], [.2, .25], [.3, .2], [.25, .3]])
    y = np.asarray([1, 1, 1, 0, 0, 0])
    mean, scale, weights, intercept = module.fit_logistic(x, y)
    model = {"mean": mean, "scale": scale, "weights": weights, "intercept": intercept}
    assert min(predict([[.87, .87], [.89, .86]], model)) > .9
    assert max(predict([[.24, .22], [.28, .26]], model)) < .1


def test_paired_readiness_requires_all_explicit_files_and_validated_artifact(tmp_path, monkeypatch):
    from mangaveyra_worker.backends.vision import VisionBackend
    from mangaveyra_worker.config import Config
    from mangaveyra_worker.vision import ccip, fashion

    monkeypatch.setattr(ccip, "ccip_available", lambda path: path is not None)
    monkeypatch.setattr(fashion, "fashion_available", lambda path: path is not None)
    path = tmp_path / "outfit.json"
    config = Config(ocr_engine="none", embedder="ccip", ccip_model_dir=tmp_path)
    assert VisionBackend(config, []).models() == ["character-lock-ccip-v1"]
    config.fashion_model_dir = tmp_path
    assert VisionBackend(config, []).models() == []
    config.outfit_model = path
    assert VisionBackend(config, []).models() == []
    value = artifact()
    value["validation"]["min_positive"] = .89
    path.write_text(json.dumps(value))
    assert VisionBackend(config, []).models() == []
    path.write_text(json.dumps(artifact()))
    assert VisionBackend(config, []).models() == ["character-lock-ccip-outfit-v3"]


@pytest.mark.parametrize("wrong_identity", [False, True])
@pytest.mark.parametrize("identities", [[.96, .6], [.6, .5]])
def test_paired_jobs_preserve_raw_evidence_and_bind_identity_weights(run_job, tmp_path, monkeypatch, wrong_identity, identities):
    from PIL import Image
    from mangaveyra_worker.backends.vision import VisionBackend
    from mangaveyra_worker.config import Config
    from mangaveyra_worker.errors import BadInput
    from mangaveyra_worker.vision import embed, outfit
    from tests.conftest import png

    identity_method = "ccip-caformer24-v1:" + "a" * 64
    combined_method = "ccip-outfit-pair-v3:" + "f" * 64
    calls = []

    class Matcher:
        artifact = {"ccip_sha256": "a" * 64, "fashion_sha256": "b" * 64}
        method = combined_method

        def __init__(self, ccip, fashion, artifact):
            assert ccip == fashion == artifact == tmp_path

        def score(self, references, candidates):
            # The matcher owns framing: the backend must send complete images.
            calls.append(len(candidates))
            assert len(candidates) == 1
            assert references[0].size == candidates[0].size == (128, 160)
            return [.97], self.method, [[.84, .86]]

    monkeypatch.setattr(outfit, "OutfitMatcher", Matcher)
    monkeypatch.setattr(embed, "score_identity", lambda *args: (identities, "ccip-caformer24-v1:" + "c" * 64 if wrong_identity else identity_method))
    config = Config(ocr_engine="none", embedder="ccip", ccip_model_dir=tmp_path, fashion_model_dir=tmp_path, outfit_model=tmp_path)
    backend = VisionBackend(config, [])
    image = png(Image.new("RGB", (128, 160), "black"))
    files = {"reference_0": image, "candidate_0": image, "candidate_1": image, "outfit_0": image}
    if wrong_identity:
        with pytest.raises(BadInput, match="differs"):
            run_job(backend, "character.score", {}, files, model="character-lock-ccip-outfit-v3")
        return
    result, _ = run_job(backend, "character.score", {}, files, model="character-lock-ccip-outfit-v3")
    assert calls == ([1] if identities[0] >= .9 else [])
    assert result.output == {
        "scores": identities, "outfit_scores": [.97 if identities[0] >= .9 else None, None], "method": combined_method,
        "identity_method": identity_method, "outfit_features": [[.84, .86] if identities[0] >= .9 else None, None],
        "feature_methods": {"ccip": identity_method, "fashion": "fashion-clip-v1:" + "b" * 64},
    }

