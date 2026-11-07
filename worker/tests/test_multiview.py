"""Protocol/lineage checks. Synthetic vectors are not image-quality evidence."""
from __future__ import annotations

import copy
import hashlib
import json
from pathlib import Path

import pytest

from mangaveyra_worker.vision.multiview import MODEL, SCHEMA, match_views, load_artifact
from mangaveyra_worker.vision.outfit import PREPROCESSING
from tests.test_outfit import artifact as pair_artifact


def artifact():
    pair = pair_artifact()
    pair.pop("independent_controls")
    refs = [hashlib.sha256(f"pose-{i}".encode()).hexdigest() for i in range(5)]
    controls = []
    for i in range(6):
        # The second gate catches a changed dark outfit even at high CCIP cosine.
        features = [[.96, .96]] * 5 if i < 2 else [[.96, -.96]] * 5
        score, pairs = match_views(features, pair)
        controls.append({
            "candidate_sha256": hashlib.sha256(f"fresh-{i}".encode()).hexdigest(),
            "features": features, "score": score, "pair_scores": pairs,
            "same_identity": i < 4, "same_outfit": i < 2 or i >= 4,
            "identity_score": .96 if i < 4 else .5,
            "identity_method": "ccip-caformer24-v1:" + pair["ccip_sha256"],
        })
    return {
        "schema": SCHEMA, "preprocessing": PREPROCESSING, "identity_gate": .9,
        "threshold": .9, "reference_limit": 5,
        "aggregation": "maximum-frozen-pair",
        "pair_model": pair, "validation_reference_sha256": refs,
        "independent_controls": controls,
    }


def test_frozen_multiview_proof_reproduces_each_clothing_comparison(tmp_path):
    path = tmp_path / "model.json"
    path.write_text(json.dumps(artifact()))
    value, digest = load_artifact(path)
    assert digest == hashlib.sha256(path.read_bytes()).hexdigest()
    assert value["independent_controls"][2]["score"] < .9
    score, pairs = match_views([[.8, 1.]], value["pair_model"])
    assert score == pairs[0] > .9


def test_checked_in_artifact_reproduces_frozen_fit_and_prospective_controls():
    root = Path(__file__).resolve().parents[1] / "examples" / "scoring"
    model, _ = load_artifact(root / "outfit-multiview-v1.json")
    frozen_path = root / "outfit-pair-frozen-fit.json"
    frozen = json.loads(frozen_path.read_bytes())
    assert hashlib.sha256(frozen_path.read_bytes()).hexdigest() == model["pair_model"]["frozen_fit_sha256"]
    for key in ("mean", "scale", "weights", "intercept", "training_image_sha256"):
        assert model["pair_model"][key] == frozen[key]
    # This known recognition false alarm stays in the proof, below 0.90.
    light = next(r for r in model["independent_controls"] if r["case"] == "outfit-light-tunic")
    assert light["identity_score"] < .9 and light["score"] < .9


def test_changed_outfit_control_keeps_low_identity_honest_but_needs_a_hard_negative(tmp_path):
    value = artifact()
    value["independent_controls"][3]["identity_score"] = .89
    path = tmp_path / "model.json"; path.write_text(json.dumps(value))
    load_artifact(path)
    value["independent_controls"][2]["identity_score"] = .89
    path.write_text(json.dumps(value))
    with pytest.raises(ValueError, match="counterexamples"): load_artifact(path)


@pytest.mark.parametrize("fault", ["policy", "threshold", "fit", "negative", "identity", "candidate-overlap", "reference-overlap", "duplicate", "feature-count", "prediction", "missing"])
def test_multiview_rejects_unproven_or_changed_models(tmp_path, fault):
    value = copy.deepcopy(artifact())
    row = value["independent_controls"][0]
    if fault == "policy": value["aggregation"] = "maximum"
    elif fault == "threshold": value["threshold"] = .8
    elif fault == "fit": value["pair_model"]["weights"][0] = 3.
    elif fault == "negative": value["independent_controls"][2]["same_outfit"] = True
    elif fault == "identity": value["independent_controls"][-1]["identity_score"] = .95
    elif fault == "candidate-overlap": row["candidate_sha256"] = value["pair_model"]["training_image_sha256"][0]
    elif fault == "reference-overlap": value["validation_reference_sha256"][0] = value["pair_model"]["heldout_image_sha256"][0]
    elif fault == "duplicate": value["independent_controls"][1]["candidate_sha256"] = row["candidate_sha256"]
    elif fault == "feature-count": row["features"] = row["features"][:1]
    elif fault == "prediction": row["pair_scores"][0] = .1
    else: value.pop("independent_controls")
    path = tmp_path / "model.json"
    path.write_text(json.dumps(value))
    with pytest.raises(ValueError): load_artifact(path)


def test_multiview_measures_each_reference_without_averaging_poses(tmp_path, monkeypatch):
    from PIL import Image
    from mangaveyra_worker.vision import ccip, fashion
    from mangaveyra_worker.vision.multiview import MultiviewMatcher

    value = artifact()
    path = tmp_path / "model.json"
    path.write_text(json.dumps(value))
    class CCIP:
        method = "ccip-caformer24-v1:" + "a" * 64
        def __init__(self, directory): pass
        def embed(self, images):
            assert len(images) == 3 and all(im.size == (512, 512) for im in images)
            return [[1., 0.], [0., 1.], [0., 1.]]
    class Fashion(CCIP):
        method = "fashion-clip-v1:" + "b" * 64
        config_sha256, processor_sha256 = "d" * 64, "e" * 64
    monkeypatch.setattr(ccip, "CcipEmbedder", CCIP)
    monkeypatch.setattr(fashion, "FashionEmbedder", Fashion)
    matcher = MultiviewMatcher(tmp_path, tmp_path, path)
    im = Image.new("RGB", (128, 160), "black")
    scores, features, pairs = matcher.score([im, im], [im])
    assert features == [[[0., 0.], [1., 1.]]]
    assert scores[0] == max(pairs[0]) > .9
    matcher.pair["fashion_sha256"] = "c" * 64
    with pytest.raises(ValueError, match="differs"): matcher.score([im], [im, im])


def test_multiview_backend_gates_identity_and_retains_both_measurements(run_job, tmp_path, monkeypatch):
    from PIL import Image
    from mangaveyra_worker.backends.vision import VisionBackend
    from mangaveyra_worker.config import Config
    from mangaveyra_worker.vision import embed, multiview
    from tests.conftest import png

    value = artifact(); calls = []
    class Matcher:
        pair = value["pair_model"]
        method = "ccip-outfit-multiview-v1:" + "f" * 64
        def __init__(self, *args): pass
        def score(self, refs, candidates):
            calls.append(len(candidates))
            assert len(refs) == 2 and len(candidates) == 1
            return [.78], [[[.9, .2], [.78, 1.]]], [[.6, .99]]
    monkeypatch.setattr(multiview, "MultiviewMatcher", Matcher)
    identity = "ccip-caformer24-v1:" + "a" * 64
    monkeypatch.setattr(embed, "score_identity", lambda *args: ([.98, .6], identity))
    config = Config(ocr_engine="none", embedder="ccip", ccip_model_dir=tmp_path, fashion_model_dir=tmp_path, outfit_model=tmp_path, outfit_mode="multiview")
    im = png(Image.new("RGB", (128, 160), "black"))
    files = {"reference_0": im, "outfit_0": im, "outfit_1": im, "candidate_0": im, "candidate_1": im}
    result, _ = run_job(VisionBackend(config, []), "character.score", {}, files, model=MODEL)
    assert calls == [1]
    out = result.output
    assert out["outfit_scores"] == [.78, None]
    assert out["outfit_reference_indices"] == [0, None]
    assert out["outfit_pair_reference_indices"] == [1, None]
    assert out["outfit_pair_features"][1] is None
    assert out["outfit_cosines"] == [[.9, .78], None]
    assert out["identity_method"] == identity and out["method"] == Matcher.method


def test_multiview_requires_complete_validated_setup_and_bounds_references(tmp_path, monkeypatch, run_job):
    from PIL import Image
    from mangaveyra_worker.backends.vision import VisionBackend
    from mangaveyra_worker.config import Config, ConfigError
    from mangaveyra_worker.errors import BadInput
    from mangaveyra_worker.vision import ccip, embed, fashion
    from tests.conftest import png

    monkeypatch.setattr(ccip, "ccip_available", lambda path: path is not None)
    monkeypatch.setattr(fashion, "fashion_available", lambda path: path is not None)
    config = Config(ocr_engine="none", embedder="ccip", ccip_model_dir=tmp_path, outfit_mode="multiview")
    assert VisionBackend(config, []).models() == []
    with pytest.raises(ConfigError): config.validate()
    path = tmp_path / "model.json"; path.write_text(json.dumps(artifact()))
    config.fashion_model_dir, config.outfit_model = tmp_path, path
    config.validate()
    assert VisionBackend(config, []).models() == [MODEL]
    monkeypatch.setattr(embed, "score_identity", lambda *args: ([.98], "ccip-caformer24-v1:" + "a" * 64))
    im = png(Image.new("RGB", (128, 160), "black"))
    files = {"reference_0": im, "candidate_0": im, **{f"outfit_{i}": im for i in range(6)}}
    with pytest.raises(BadInput, match="multiview reference"):
        run_job(VisionBackend(config, []), "character.score", {}, files, model=MODEL)
