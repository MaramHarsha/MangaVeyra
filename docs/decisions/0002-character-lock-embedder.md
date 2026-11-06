# ADR 0002: Character Lock embedding baseline

- Status: Accepted, benchmark-gated
- Date: 2026-11-07

## Decision

Use `facebook/dinov2-small` as the first commercially usable Character Lock
embedding candidate. Pin the exact model revision and checksums, load it through
standard Transformers APIs without `trust_remote_code`, and preserve the worker's
current contract of L2-normalized vectors or `None` per crop.

The automatic order becomes DINOv2 Small, then OpenCLIP, then the structural
fallback. InsightFace `buffalo_l` is removed from the default commercial path:
InsightFace's source is MIT, but its published pretrained model packs are
non-commercial unless separately licensed.

This is a benchmark-first choice, not a claim that frozen DINOv2 already solves
anime character identity. A model-specific threshold replaces the existing
universal `0.8` only after validation. If DINOv2 fails the gates below, OpenCLIP
remains a fallback while the team evaluates a rights-cleared metric head trained on
original or explicitly licensed characters.

## Evidence and licensing boundary

- DINOv2 code and published model weights are Apache-2.0:
  <https://github.com/facebookresearch/dinov2/blob/main/README.md#license>
- Its model card describes 21M-parameter ViT-S/14 features and retrieval use:
  <https://github.com/facebookresearch/dinov2/blob/main/MODEL_CARD.md>
- DINOv2's training corpus includes curated datasets and retrieved web images; the
  weight licence does not grant blanket rights to every source image:
  <https://arxiv.org/html/2304.07193#S3>
- InsightFace distinguishes MIT source code from non-commercial pretrained packs:
  <https://github.com/deepinsight/insightface#license>

Code licence, weight licence, and training-data provenance are tracked separately
in the SBOM. This ADR is not legal indemnification.

## Validation gate

Build a redistributable benchmark of roughly 60 original, commissioned, or
explicitly licensed characters, split by identity and artist/generator. Include
pose, expression, camera distance, outfit, monochrome/screentone, line art, chibi,
occlusion, and hard negatives with similar visual traits.

The MVP gate is:

- 100% embedding coverage on valid crops.
- ROC-AUC at least 0.90.
- Rank-1 retrieval at least 0.85 with three references.
- TPR at least 0.80 at 1% FPR on hard negatives.
- No primary metric below the existing OpenCLIP baseline, with a material gain on
  monochrome or style-shift subsets.
- Model artifact below 150 MB, with recorded CPU/GPU latency and memory.

Benchmark CLS, mean patch pooling, and normalized CLS plus mean-patch pooling before
freezing the representation. Calibrate the displayed 0–1 score per selected method;
cosine similarity is not presented as a probability.

## Deferred alternatives

SigLIP 2 and Nomic Embed Vision remain general-purpose retrieval alternatives but
are larger and have no demonstrated advantage for this task. Task-specific anime
models with ambiguous weight licences, pickle-only artifacts, or unverified
training provenance are evaluation-only and cannot ship by default.
