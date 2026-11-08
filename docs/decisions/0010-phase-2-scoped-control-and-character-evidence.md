# ADR 0010: Scoped control and Character Lock evidence

- Status: Accepted implementation; chapter-quality acceptance remains open.
- Date: 2026-11-08
- Contract: [plan 0006](../plans/0006-phase-2-consistency-and-control.md).
- Complements ADRs 0003, 0007 and 0009. Remote routes, tariffs and per-call reservations are unchanged.

## Scoped control

`gen.ControlService` extends the first-playable contract without widening its existing mocks. `page.generate`
stages all independent art components before one atomic commit and one saved revision per affected page.
It reads only the immediately preceding/following pages as continuity context; their revisions are fenced and
they are never modified. Locked art, permissions, target versions, bible fingerprints, parent leases and
cancellation are checked before dispatch and again at commit. A late failure discards the complete staged result.
Lettering and panel polygons remain structured data.

`character.repair` has one explicit chapter scope and a bounded list of at most 24 selected character layers.
Each target's ancestry, character, version and locks are resolved on the server. Existing art is the composition
source; references belong only to that character. Other layers and text remain unchanged. This is an AI edit,
so the creator reviews visual fidelity. Exact pixel preservation belongs to masked region editing, not a
whole-character repair. Identical active coordinators are reused; overlapping different coordinators conflict.

Worker calls use declared, bounded component slots, immutable attempt/digest identities and narrower child scopes.
Scoring children cannot be confused with image children during a resumed call. All billable requests continue
through the existing reservation ledger. Quotes cover the bounded operation before the creator starts it.

Basic D10 compares the saved current proof with an immutable selected snapshot. Reads are scoped to the actual
project/chapter/page; restoring creates a new revision through the existing optimistic fence. The latest 50
snapshots are visible. Named branches and merging remain designed Soon controls for Phase 6.

Identical active requests are reused through database guards only (row-locked coordinator checks and the jobs dedupe
index, migration 0204); no process-local lock is relied on.

## Learned evidence and licensing

The optional learned scorer is locally provisioned **DINOv2 Small** (`facebook/dinov2-small`, Apache-2.0).
Sources: [checkpoint and license](https://huggingface.co/facebook/dinov2-small),
[official model card](https://github.com/facebookresearch/dinov2/blob/main/MODEL_CARD.md),
[Transformers API](https://huggingface.co/docs/transformers/model_doc/dinov2).
Runtime requires an explicit model directory, safetensors, offline loading and no remote model code.
No model downloads occur during application startup or scoring. InsightFace/CLIP are legacy explicit worker
opt-ins, never automatically selected for authoritative Character Lock evidence. Structural/mock outputs are
not accepted as learned scores.

`character_assessments` records the original job, immutable candidate asset, reference fingerprint, separate
identity/outfit similarities and method `dinov2-small-v1:<weights SHA256>`. The reference fingerprint includes
character name/look, reference kinds/assets/checksums and the default outfit's name, description and image.
Changing these invalidates old evidence and trained artifacts. Pages and D8 candidates resolve evidence against
the current bible; persisted legacy percentages are not displayed as measurements.

Individual kept front/side/back views are preferred to a montage for scoring; generation still receives the kept
sheet first. Embeddings compare a candidate with the reference centroid by cosine similarity. The optional outfit
comparison uses an explicitly kept outfit image/default outfit reference and its lower-body crop. Missing outfit
evidence is reported separately. No fake percentage fills missing evidence. The aggregate uses the lower of the
available identity/outfit measurements; D12 lists individual values below the existing **0.90** policy threshold.

DINOv2 is a general visual feature model, not a calibrated manga identity verifier. Similarity is not an identity
probability or a guarantee. The approved live run produced real manga but failed the 30-page threshold, including
after representative repairs. Preserve that initial failure rather than describing it as a passing quality result. See the
[live acceptance plan](../plans/0007-phase-2-live-acceptance.md) and [QA record](../qa/phase-2-control-2026-11-08.md).
The later explicit anime/outfit-view profile is defined in [ADR 0011](0011-explicit-anime-scoring-and-outfit-validation.md);
its independently validated thirty-page pass and unchanged page-7 isolation are recorded in the
[completion evidence](../qa/phase-2-completion-2026-11-08.md). The DINOv2 measurements themselves remain unchanged.

## Optional training, poses and sketch conversion

Training requires three different kept images, an explicitly configured compatible SDXL/Illustrious worker and
a current reference revision. Mock/malformed safetensors, incompatible base models and stale artifacts are refused.
Only an owned artifact with compatible base-model/reference metadata becomes ready and can be attached to a later
worker generation request. This is an artifact/protocol guarantee; actual GPU training quality is not validated
on this machine. Successful training sends a localized, safe notification linking to C3.

Normalized 13-joint poses have finite bounds and server validation. Existing opaque legacy pose metadata remains
saveable but is not executable input. Presets and the keyboard/pointer stick editor use the same schema. The server
renders a conventional OpenPose guide; configured native models must have an OpenPose ControlNet and unsupported
workers fail explicitly. Hosted routes receive the guide as a control reference. Native IP-Adapter conditioning
retains up to eight references in one bounded guide instead of silently dropping reference 2 onward.

D4 uploads/drawings are bounded to 16 MiB and 64–4096 pixels per side. Conversion retains the saved polygons,
cast and lettering, uses one page scope, and applies the selected composition strength and finish. Source sketches
are cropped to each existing polygon's bounds. Existing layout/cast/text are used; automatic sketch detection and
camera capture remain honest Soon states. No new palette, font, icon set or token is introduced.

`local_only` blocks both scoring/training bridges and all hosted generation. Worker inputs are owned, private,
temporary assets; secrets and private job inputs do not appear in public queue/notification views.
