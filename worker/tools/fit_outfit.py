"""Fit the plan-0008 outfit relationship experiment, entirely offline.

Run only against the independent wardrobe studies, never acceptance pages.
A failed held-out result produces an unusable candidate artifact, not a pass.
"""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import mangaveyra_worker  # noqa: E402,F401 - establish privacy gates before optional imports
import numpy as np  # noqa: E402
from PIL import Image  # noqa: E402

from mangaveyra_worker.vision.ccip import CcipEmbedder  # noqa: E402
from mangaveyra_worker.vision.embed import cosine  # noqa: E402
from mangaveyra_worker.vision.fashion import FashionEmbedder  # noqa: E402
from mangaveyra_worker.vision.outfit import FEATURES, IDENTITY_GATE, PREPROCESSING, SCHEMA, framed_body, predict  # noqa: E402


def fit_logistic(x, y):
    """Fixed balanced logistic recipe; no held-out/chapter data affect fitting."""
    mean, scale = x.mean(axis=0), np.maximum(x.std(axis=0), 1e-6)
    design = np.column_stack((np.ones(len(x)), (x - mean) / scale))
    weights = np.where(y == 1, len(y) / (2 * y.sum()), len(y) / (2 * (len(y) - y.sum())))
    penalty = np.diag((0., .001, .001))
    beta = np.zeros(3, dtype=np.float64)

    def loss(b):
        z = design @ b
        return np.mean(weights * (np.logaddexp(0, z) - y * z)) + .5 * b @ penalty @ b

    for _ in range(100):
        z = np.clip(design @ beta, -60, 60)
        probabilities = 1 / (1 + np.exp(-z))
        gradient = design.T @ (weights * (probabilities - y)) / len(y) + penalty @ beta
        if np.linalg.norm(gradient) < 1e-8:
            break
        curvature = weights * probabilities * (1 - probabilities)
        hessian = design.T @ (curvature[:, None] * design) / len(y) + penalty + np.eye(3) * 1e-9
        delta = np.linalg.solve(hessian, gradient)
        step = 1.
        for _ in range(20):
            if loss(beta - step * delta) < loss(beta):
                break
            step *= .5
        beta -= step * delta
    return mean.tolist(), scale.tolist(), beta[1:].tolist(), float(beta[0])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--data", type=Path, required=True)
    parser.add_argument("--ccip", type=Path, required=True)
    parser.add_argument("--fashion", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--freeze-only", action="store_true", help="fit training images before reading fresh validation")
    parser.add_argument("--reviewed", action="store_true", help="images were visually inspected and labels verified")
    args = parser.parse_args()
    if not args.reviewed:
        parser.error("inspect the complete independent study images before fitting")
    if not str(args.data.resolve()).startswith(("/private/tmp/mv-phase2-", "/tmp/mv-phase2-")):
        parser.error("this experiment requires the isolated plan-0008 study directory")
    raw = (args.data / "outfit-dataset.json").read_bytes()
    dataset = json.loads(raw)
    if len(dataset["people"]) != 9 or len(dataset["samples"]) != 36:
        raise ValueError("independent study contract changed")
    fresh_raw = b"" if args.freeze_only else (args.data / "outfit-validation.json").read_bytes()
    fresh = {"people": [], "samples": []} if args.freeze_only else json.loads(fresh_raw)
    if not args.freeze_only and (len(fresh["people"]) != 3 or len(fresh["samples"]) != 12 or any(p["split"] != "heldout" for p in fresh["people"])):
        raise ValueError("fresh validation contract changed")
    if {p["name"] for p in dataset["people"]} & {p["name"] for p in fresh["people"]}:
        raise ValueError("fresh identities overlap development studies")
    people, samples, paths = [], [], []
    for source, prefix, split in ((dataset, "outfit-study", "train"), (fresh, "outfit-validation", "heldout")):
        mapping = {}
        for i, person in enumerate(source["people"]):
            if person["split"] == split:
                mapping[i] = len(people)
                people.append(person)
        for i, sample in enumerate(source["samples"]):
            if sample["person"] in mapping:
                samples.append({**sample, "person": mapping[sample["person"]]})
                paths.append(args.data / f"{prefix}-{i+1:02d}.png")
    expected_people, expected_images = (6, 24) if args.freeze_only else (9, 36)
    if len(people) != expected_people or len(samples) != expected_images:
        raise ValueError("training/fresh split changed")
    if not all(s["asset_id"] for s in samples):
        raise ValueError("independent study generation is incomplete")
    images, hashes = [], []
    for path in paths:
        data = path.read_bytes()
        hashes.append(hashlib.sha256(data).hexdigest())
        images.append(framed_body(Image.open(path).copy()))
    if len(set(hashes)) != expected_images:
        raise ValueError("repeated study images cannot establish validation")
    args.out.mkdir(parents=True, exist_ok=True)
    cache = args.out / "study-features.npz"
    ccip, fashion = CcipEmbedder(args.ccip), FashionEmbedder(args.fashion)
    ccip._load()
    fashion._load()
    lineage = {
        "images": hashes, "ccip": ccip.method, "fashion": fashion.method,
        "preprocessing": PREPROCESSING,
        "fashion_config_sha256": fashion.config_sha256,
        "fashion_processor_sha256": fashion.processor_sha256,
    }
    if cache.exists() and json.loads((args.out / "feature-lineage.json").read_text()) == lineage:
        found = np.load(cache)
        cv, fv = found["ccip"], found["fashion"]
    else:
        cv, fv = np.asarray(ccip.embed(images)), np.asarray(fashion.embed(images))
        np.savez(cache, ccip=cv, fashion=fv)
        (args.out / "feature-lineage.json").write_text(json.dumps(lineage, indent=2))

    def pairs(split):
        rows = []
        for i, ref in enumerate(samples):
            person = people[ref["person"]]
            if ref["pose"] != 0 or person["split"] != split:
                continue
            for j, candidate in enumerate(samples):
                other = people[candidate["person"]]
                if i == j or other["split"] != split:
                    continue
                rows.append({
                    "reference": i + 1, "candidate": j + 1,
                    "same_outfit": person["group"] == other["group"],
                    "features": [cosine(cv[i], cv[j]), cosine(fv[i], fv[j])],
                })
        return rows

    train, heldout = pairs("train"), pairs("heldout")
    x = np.asarray([p["features"] for p in train], dtype=np.float64)
    y = np.asarray([p["same_outfit"] for p in train], dtype=np.float64)
    mean, scale, weights, intercept = fit_logistic(x, y)
    frozen_path = args.out / "frozen-fit.json"
    frozen = {
        "schema": SCHEMA, "preprocessing": PREPROCESSING,
        "mean": mean, "scale": scale, "weights": weights, "intercept": intercept,
        "training_image_sha256": [hashes[i] for i, s in enumerate(samples) if people[s["person"]]["split"] == "train"],
        "recipe": {"balanced_logistic_l2": .001, "max_iterations": 100},
    }
    if frozen_path.exists():
        saved = json.loads(frozen_path.read_text())
        numeric = ("mean", "scale", "weights", "intercept")
        metadata_matches = all(saved.get(k) == v for k, v in frozen.items() if k not in numeric)
        # Python and NumPy scalar accumulation can differ at ~1e-14. Never
        # replace the original parameters; accept only numerical reproduction.
        if not metadata_matches or not all(np.allclose(saved.get(k), frozen[k], rtol=0, atol=1e-10) for k in numeric):
            raise ValueError("fit differs from the training-only freeze; do not tune against validation")
        mean, scale, weights, intercept = (saved[k] for k in numeric)
    elif args.freeze_only:
        frozen_path.write_text(json.dumps(frozen, indent=2) + "\n")
    else:
        raise ValueError("freeze the training-only fit before reading validation")
    if args.freeze_only:
        print("Frozen training fit; fresh validation was not read")
        return 0
    artifact = {
        "schema": SCHEMA, "identity_gate": IDENTITY_GATE, "preprocessing": PREPROCESSING, "features": FEATURES, "threshold": .9,
        "mean": mean, "scale": scale, "weights": weights, "intercept": intercept,
        "ccip_sha256": ccip.method.split(":")[1], "fashion_sha256": fashion.method.split(":")[1],
        "fashion_config_sha256": fashion.config_sha256,
        "fashion_processor_sha256": fashion.processor_sha256,
        "dataset_sha256": hashlib.sha256(raw + b"\0" + fresh_raw).hexdigest(),
        "training_identities": [p["name"] for p in people if p["split"] == "train"],
        "heldout_identities": [p["name"] for p in people if p["split"] == "heldout"],
        "training_image_sha256": [hashes[i] for i,s in enumerate(samples) if people[s["person"]]["split"]=="train"],
        "heldout_image_sha256": [hashes[i] for i,s in enumerate(samples) if people[s["person"]]["split"]=="heldout"],
        "recipe": {"balanced_logistic_l2": .001, "max_iterations": 100, "features": 2},
        "frozen_fit_sha256": hashlib.sha256(frozen_path.read_bytes()).hexdigest(),
        "review": "Codex visual inspection of the original study images",
    }
    for row, score in zip(heldout, predict([p["features"] for p in heldout], artifact)):
        row["score"] = score
    positive = [p["score"] for p in heldout if p["same_outfit"]]
    negative = [p["score"] for p in heldout if not p["same_outfit"]]
    artifact["validation"] = {
        "split": "disjoint-identities", "positives": len(positive), "negatives": len(negative),
        "min_positive": min(positive), "max_negative": max(negative),
    }
    artifact["heldout_pairs"] = [{
        "reference_sha256": hashes[row["reference"]-1],
        "candidate_sha256": hashes[row["candidate"]-1],
        "same_outfit": row["same_outfit"], "features": row["features"], "score": row["score"],
    } for row in heldout]
    passed = min(positive) >= .9 and max(negative) < .9
    (args.out / "candidate.json").write_text(json.dumps(artifact, indent=2) + "\n")
    (args.out / "predictions.json").write_text(json.dumps({"training": train, "heldout": heldout}, indent=2))
    print(json.dumps({"heldout_pass": passed, **artifact["validation"], "weights": weights, "intercept": intercept}, indent=2))
    return 0 if passed else 2


if __name__ == "__main__":
    raise SystemExit(main())
