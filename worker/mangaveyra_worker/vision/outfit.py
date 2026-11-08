"""A small, auditable outfit relationship model over two learned features.

This is a learned match score, not a raw cosine or a general identity guarantee.
The externally provisioned artifact must bind both feature-model checksums and
record a disjoint-identity held-out validation at the fixed 0.90 threshold.
"""
from __future__ import annotations

import hashlib
import json
import math
from pathlib import Path
from typing import Sequence


FEATURES = ["ccip_lower_body_cosine", "fashion_lower_body_cosine"]
SCHEMA = "mangaveyra.outfit-pair.v3"
PREPROCESSING = "subject-body35-contain512-v2"
IDENTITY_GATE = .9


def _digest(value) -> bool:
    return isinstance(value, str) and len(value) == 64 and all(c in "0123456789abcdef" for c in value)


def body(image):
    return image.crop((0, int(image.height * .35), image.width, image.height))


def framed_body(image):
    """Keep the complete outfit region despite differing layer/reference framing."""
    from PIL import Image, ImageChops, ImageOps

    rgba = ImageOps.exif_transpose(image).convert("RGBA")
    white = Image.new("RGBA", rgba.size, "white")
    white.alpha_composite(rgba)
    rgb = white.convert("RGB")
    foreground = ImageChops.difference(rgb, Image.new("RGB", rgb.size, "white")).convert("L")
    box = foreground.point(lambda n: 255 if n > 12 else 0).getbbox()
    if box is None or (box[2] - box[0]) * (box[3] - box[1]) < 16:
        raise ValueError("no visible outfit subject")
    subject = rgb.crop(box)
    return ImageOps.pad(body(subject), (512, 512), Image.Resampling.LANCZOS, color="white")


def predict(features: Sequence[Sequence[float]], artifact: dict) -> list[float]:
    out = []
    for row in features:
        if len(row) != 2 or not all(isinstance(x, (int, float)) and not isinstance(x, bool) and math.isfinite(x) and -1.00001 <= x <= 1.00001 for x in row):
            raise ValueError("invalid outfit feature vector")
        logit = artifact["intercept"] + sum(
            artifact["weights"][i] * (row[i] - artifact["mean"][i]) / artifact["scale"][i]
            for i in range(2)
        )
        out.append(1 / (1 + math.exp(-max(-60, min(60, logit)))))
    return out


def load_artifact(path: str | Path, *, require_controls: bool = True) -> tuple[dict, str]:
    path = Path(path)
    if path.stat().st_size > 64 * 1024:
        raise ValueError("outfit artifact exceeds size bound")
    raw = path.read_bytes()
    value = json.loads(raw)
    validate_artifact(value, require_controls=require_controls)
    return value, hashlib.sha256(raw).hexdigest()


def validate_artifact(value: dict, *, require_controls: bool = True) -> None:
    """Validate a frozen pair model, including its reproducible held-out proof."""
    if not isinstance(value, dict):
        raise ValueError("invalid outfit artifact object")
    if value.get("schema") != SCHEMA or value.get("identity_gate") != IDENTITY_GATE or value.get("preprocessing") != PREPROCESSING or value.get("features") != FEATURES or value.get("threshold") != .9:
        raise ValueError("unsupported outfit model contract")
    for key in ("mean", "scale", "weights"):
        if not isinstance(value.get(key), list) or len(value[key]) != 2:
            raise ValueError("invalid outfit model parameters")
        if not all(isinstance(x, (int, float)) and math.isfinite(x) and abs(x) <= 10000 for x in value[key]):
            raise ValueError("invalid outfit model parameters")
    intercept = value.get("intercept")
    if any(x <= 0 for x in value["scale"]) or not isinstance(intercept, (int, float)) or not math.isfinite(intercept) or abs(intercept) > 10000:
        raise ValueError("invalid outfit model scale")
    checks = value.get("validation", {})
    if not isinstance(checks, dict):
        raise ValueError("invalid outfit validation object")
    if checks.get("split") != "disjoint-identities" or checks.get("positives", 0) < 6 or checks.get("negatives", 0) < 12:
        raise ValueError("missing independent outfit validation")
    low, high = checks.get("min_positive", 0), checks.get("max_negative", 1)
    if not all(isinstance(x, (int, float)) and math.isfinite(x) and 0 <= x <= 1 for x in (low, high)):
        raise ValueError("invalid outfit validation scores")
    if low < .9 or high >= .9:
        raise ValueError("outfit validation did not meet the fixed threshold")
    train, heldout = value.get("training_identities", []), value.get("heldout_identities", [])
    if not isinstance(train, list) or not isinstance(heldout, list):
        raise ValueError("invalid outfit identity split")
    if not all(isinstance(x, str) and x for x in [*train, *heldout]):
        raise ValueError("invalid outfit validation identities")
    if len(set(train)) < 3 or len(set(heldout)) < 3:
        raise ValueError("missing independent outfit identities")
    if set(train) & set(heldout):
        raise ValueError("outfit validation identities overlap training")
    for key in ("ccip_sha256", "fashion_sha256", "fashion_config_sha256", "fashion_processor_sha256", "dataset_sha256", "frozen_fit_sha256"):
        if not _digest(value.get(key)):
            raise ValueError("missing outfit model lineage")
    train_images, held_images = value.get("training_image_sha256", []), value.get("heldout_image_sha256", [])
    if not isinstance(train_images, list) or not isinstance(held_images, list):
        raise ValueError("invalid outfit validation images")
    if not all(_digest(x) for x in [*train_images, *held_images]):
        raise ValueError("invalid outfit image lineage")
    if len(set(train_images)) < 18 or len(set(held_images)) < 9:
        raise ValueError("missing independent outfit images")
    if set(train_images) & set(held_images):
        raise ValueError("outfit validation images overlap training")
    pairs = value.get("heldout_pairs", [])
    if not isinstance(pairs, list) or not 18 <= len(pairs) <= 200:
        raise ValueError("missing reproducible outfit validation")
    positives, negatives, seen = [], [], set()
    for row in pairs:
        if not isinstance(row, dict):
            raise ValueError("invalid outfit validation pair")
        reference, candidate = row.get("reference_sha256"), row.get("candidate_sha256")
        if reference not in held_images or candidate not in held_images or reference == candidate:
            raise ValueError("invalid outfit validation pair")
        if (reference, candidate) in seen:
            raise ValueError("repeated outfit validation pair")
        seen.add((reference, candidate))
        features = row.get("features")
        if not isinstance(features, list):
            raise ValueError("missing outfit validation features")
        score = predict([features], value)[0]
        recorded = row.get("score")
        if not isinstance(recorded, (int, float)) or not math.isfinite(recorded) or abs(score - recorded) > 1e-8:
            raise ValueError("outfit validation does not match parameters")
        if row.get("same_outfit") is True:
            positives.append(score)
        elif row.get("same_outfit") is False:
            negatives.append(score)
        else:
            raise ValueError("missing outfit validation label")
    if len(positives) != checks["positives"] or len(negatives) != checks["negatives"]:
        raise ValueError("outfit validation counts do not reproduce")
    if min(positives) < .9 or max(negatives) >= .9 or abs(min(positives) - low) > 1e-8 or abs(max(negatives) - high) > 1e-8:
        raise ValueError("outfit validation did not reproduce")
    if require_controls:
        controls = value.get("independent_controls", [])
        if not isinstance(controls, list) or not 6 <= len(controls) <= 24:
            raise ValueError("missing independent outfit controls")
        positive_count, negative_count, other_identity_count, candidates = 0, 0, 0, set()
        for row in controls:
            if not isinstance(row, dict):
                raise ValueError("invalid independent outfit control")
            reference, candidate = row.get("reference_sha256"), row.get("candidate_sha256")
            if not _digest(reference) or not _digest(candidate) or reference == candidate:
                raise ValueError("invalid independent outfit control images")
            if reference in train_images + held_images or candidate in train_images + held_images or candidate in candidates:
                raise ValueError("outfit controls overlap training or held-out images")
            candidates.add(candidate)
            features, recorded = row.get("features"), row.get("score")
            if not isinstance(features, list) or not isinstance(recorded, (int, float)) or not math.isfinite(recorded):
                raise ValueError("missing independent outfit control features")
            score = predict([features], value)[0]
            if abs(score - recorded) > 1e-8:
                raise ValueError("outfit controls do not match frozen parameters")
            identity = row.get("identity_score")
            if not isinstance(identity, (int, float)) or not math.isfinite(identity) or not 0 <= identity <= 1:
                raise ValueError("missing independent identity control measurement")
            if row.get("identity_method") != "ccip-caformer24-v1:" + value["ccip_sha256"]:
                raise ValueError("independent identity control model mismatch")
            if row.get("same_identity") is False:
                if identity >= IDENTITY_GATE or row.get("same_outfit") is not True:
                    raise ValueError("different-identity uniform controls failed")
                other_identity_count += 1
                continue
            if row.get("same_identity") is not True:
                raise ValueError("missing independent identity control label")
            if row.get("same_outfit") is True and score >= .9 and identity >= IDENTITY_GATE:
                positive_count += 1
            elif row.get("same_outfit") is False and score < .9:
                negative_count += 1
            else:
                raise ValueError("independent outfit controls failed")
        if positive_count < 2 or negative_count < 2 or other_identity_count < 2:
            raise ValueError("independent outfit controls lack counterexamples")


class OutfitMatcher:
    """Frozen relationship model with both feature-model lineages enforced."""

    def __init__(self, ccip_dir, fashion_dir, artifact_path, *, validation_only: bool = False) -> None:
        from mangaveyra_worker.vision.ccip import CcipEmbedder
        from mangaveyra_worker.vision.fashion import FashionEmbedder

        self.artifact, digest = load_artifact(artifact_path, require_controls=not validation_only)
        self.ccip, self.fashion = CcipEmbedder(ccip_dir), FashionEmbedder(fashion_dir)
        self.method = "ccip-outfit-pair-v3:" + digest

    def score(self, references, candidates):
        from mangaveyra_worker.vision.embed import centroid, cosine

        if not references or not candidates:
            raise ValueError("outfit matching requires references and candidates")
        images = [framed_body(image) for image in [*references, *candidates]]
        cv, fv = self.ccip.embed(images), self.fashion.embed(images)
        if self.ccip.method.split(":")[1] != self.artifact["ccip_sha256"]:
            raise ValueError("outfit CCIP model checksum differs from training")
        if self.fashion.method.split(":")[1] != self.artifact["fashion_sha256"]:
            raise ValueError("outfit FashionCLIP model checksum differs from training")
        if self.fashion.config_sha256 != self.artifact["fashion_config_sha256"] or self.fashion.processor_sha256 != self.artifact["fashion_processor_sha256"]:
            raise ValueError("outfit FashionCLIP preprocessing differs from training")
        n = len(references)
        c, f = centroid(cv[:n]), centroid(fv[:n])
        features = [[cosine(c, a), cosine(f, b)] for a, b in zip(cv[n:], fv[n:])]
        return predict(features, self.artifact), self.method, features
