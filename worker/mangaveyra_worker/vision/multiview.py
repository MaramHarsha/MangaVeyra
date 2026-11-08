"""Frozen clothing relationship matching against individually kept pose views.

Identity is measured separately. An outfit passes only when the frozen clothing
relationship model matches a kept view. Raw feature cosines remain evidence. The model
artifact must reproduce fresh pose, identity and changed-costume controls.
"""
from __future__ import annotations

import hashlib
import json
import math
from pathlib import Path

from mangaveyra_worker.vision.outfit import (
    IDENTITY_GATE, PREPROCESSING, _digest, framed_body, predict, validate_artifact,
)

SCHEMA = "mangaveyra.outfit-multiview.v1"
METHOD = "ccip-outfit-multiview-v1"
MODEL = "character-lock-ccip-outfit-multiview-v1"
REFERENCE_LIMIT = 5


def match_views(features: list[list[float]], pair_model: dict) -> tuple[float, list[float]]:
    if not 1 <= len(features) <= REFERENCE_LIMIT:
        raise ValueError("multiview requires one to five outfit references")
    pairs = predict(features, pair_model)
    return max(pairs), pairs


def load_artifact(path: str | Path) -> tuple[dict, str]:
    path = Path(path)
    if path.stat().st_size > 128 * 1024:
        raise ValueError("multiview artifact exceeds size bound")
    raw = path.read_bytes()
    value = json.loads(raw)
    if not isinstance(value, dict) or value.get("schema") != SCHEMA or value.get("preprocessing") != PREPROCESSING:
        raise ValueError("unsupported multiview model contract")
    if value.get("identity_gate") != IDENTITY_GATE or value.get("threshold") != .9 or value.get("reference_limit") != REFERENCE_LIMIT or value.get("aggregation") != "maximum-frozen-pair":
        raise ValueError("unsafe multiview matching policy")
    pair = value.get("pair_model")
    validate_artifact(pair, require_controls=False)
    refs = value.get("validation_reference_sha256")
    if not isinstance(refs, list) or not 1 <= len(refs) <= REFERENCE_LIMIT or len(set(refs)) != len(refs) or not all(_digest(r) for r in refs):
        raise ValueError("missing multiview validation reference lineage")
    used = set(pair["training_image_sha256"] + pair["heldout_image_sha256"])
    if used.intersection(refs):
        raise ValueError("multiview validation references overlap fitting data")
    controls = value.get("independent_controls")
    if not isinstance(controls, list) or not 6 <= len(controls) <= 24:
        raise ValueError("missing fresh multiview controls")
    positive, negative, negative_after_identity, other, seen = 0, 0, 0, 0, set()
    for row in controls:
        if not isinstance(row, dict):
            raise ValueError("invalid multiview control")
        candidate = row.get("candidate_sha256")
        if not _digest(candidate) or candidate in used or candidate in refs or candidate in seen:
            raise ValueError("multiview controls overlap reference or fitting images")
        seen.add(candidate)
        features = row.get("features")
        if not isinstance(features, list) or len(features) != len(refs):
            raise ValueError("missing per-view control features")
        score, pairs = match_views(features, pair)
        recorded = row.get("score")
        if not isinstance(recorded, (int, float)) or not math.isfinite(recorded) or abs(score - recorded) > 1e-8:
            raise ValueError("multiview control does not reproduce frozen parameters")
        recorded_pairs = row.get("pair_scores")
        if not isinstance(recorded_pairs, list) or len(recorded_pairs) != len(pairs) or any(not isinstance(v, (int, float)) or not math.isfinite(v) or abs(v - p) > 1e-8 for v, p in zip(recorded_pairs, pairs, strict=True)):
            raise ValueError("multiview pair control does not reproduce")
        identity = row.get("identity_score")
        if not isinstance(identity, (int, float)) or not math.isfinite(identity) or not 0 <= identity <= 1 or row.get("identity_method") != "ccip-caformer24-v1:" + pair["ccip_sha256"]:
            raise ValueError("invalid multiview identity control")
        if row.get("same_identity") is False:
            if identity >= IDENTITY_GATE or row.get("same_outfit") is not True:
                raise ValueError("different-person uniform control failed")
            other += 1
        elif row.get("same_identity") is True and row.get("same_outfit") is True and identity >= IDENTITY_GATE and score >= .9:
            positive += 1
        elif row.get("same_identity") is True and row.get("same_outfit") is False and score < .9:
            # A changed costume may also reduce identity confidence. Keep that
            # measurement honest; never require a failed match to look like a
            # correct one. At least one hard negative must pass identity so the
            # clothing checker, independently, is required to reject it.
            negative += 1
            negative_after_identity += int(identity >= IDENTITY_GATE)
        else:
            raise ValueError("fresh multiview outfit control failed")
    if min(positive, negative, other) < 2 or negative_after_identity < 1:
        raise ValueError("multiview validation lacks independent counterexamples")
    return value, hashlib.sha256(raw).hexdigest()


class MultiviewMatcher:
    def __init__(self, ccip_dir, fashion_dir, artifact_path) -> None:
        from mangaveyra_worker.vision.ccip import CcipEmbedder
        from mangaveyra_worker.vision.fashion import FashionEmbedder

        self.artifact, digest = load_artifact(artifact_path)
        self.pair = self.artifact["pair_model"]
        self.ccip, self.fashion = CcipEmbedder(ccip_dir), FashionEmbedder(fashion_dir)
        self.method = METHOD + ":" + digest

    def score(self, references, candidates):
        from mangaveyra_worker.vision.embed import cosine

        if not candidates or not 1 <= len(references) <= REFERENCE_LIMIT:
            raise ValueError("multiview requires candidates and one to five outfit references")
        images = [framed_body(im) for im in [*references, *candidates]]
        cv, fv = self.ccip.embed(images), self.fashion.embed(images)
        if self.ccip.method != "ccip-caformer24-v1:" + self.pair["ccip_sha256"] or self.fashion.method != "fashion-clip-v1:" + self.pair["fashion_sha256"]:
            raise ValueError("multiview feature model differs from frozen fit")
        if self.fashion.config_sha256 != self.pair["fashion_config_sha256"] or self.fashion.processor_sha256 != self.pair["fashion_processor_sha256"]:
            raise ValueError("multiview clothing preprocessing differs from frozen fit")
        n = len(references)
        features = [[[cosine(cv[i], a), cosine(fv[i], b)] for i in range(n)] for a, b in zip(cv[n:], fv[n:], strict=True)]
        values, pairs = [], []
        for row in features:
            value, measured = match_views(row, self.pair)
            values.append(value)
            pairs.append(measured)
        return values, features, pairs
