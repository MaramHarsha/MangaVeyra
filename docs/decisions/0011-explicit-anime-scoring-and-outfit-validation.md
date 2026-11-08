# ADR 0011: Explicit anime scoring and validated outfit artifacts

- Status: Accepted explicit models and validated outfit-view matching; Phase 2 chapter acceptance passed.
- Date: 2026-11-08
- Contract: [plan 0006](../plans/0006-phase-2-consistency-and-control.md), [validation plan 0008](../plans/0008-outfit-validation.md), [reference coverage plan 0009](../plans/0009-outfit-reference-coverage.md).
- Extends [ADR 0010](0010-phase-2-scoped-control-and-character-evidence.md).

## Explicit models and current evidence

DINOv2 Small remains the automatic learned option. Explicit `MV_EMBEDDER=ccip` selects the locally provisioned
official CCIP caformer-24 feature graph. Its OpenRAIL-tagged weights are supplied separately; no weights or
downloads ship with the application. Source: [official ONNX exports](https://huggingface.co/deepghs/ccip_onnx),
revision `eb2acdd29af1703388d3d0c04221add322bc9110`. The selected graph SHA256 is
`4ea118d16496274f4f6e08d3afc768cc592389e8f7f32f8732ce2215c228ac5f`.

The published full-image CCIP transform is retained. Identity compares reference-centroid/candidate cosine;
outfit evidence remains a separate lower-body comparison. `character-lock-ccip-v1` returns
`ccip-caformer24-v1:<weights SHA256>`. Raw measurements retain precision before the server applies 0.90.
Whole-percent labels cannot round a failing measurement into a visible pass.

The server binds returned methods to requested model profiles and freezes one method across an assessment's
batches. A newly selected model has no displayed result until it has evidence. Partial assessments from different
models/weight methods are never averaged. Offline reads retain the latest attributable method. C3 names it;
C3, D8, D12 and page views use the same current asset/reference/method evidence.
The designed C3 chart has at most twelve recent real measurements in chronological order, uses the same lower
identity/outfit value and is retired with that method. Its bounded authorized query contains no placeholder data.

Worker initialization disables ONNX Runtime and Hugging Face telemetry before optional library imports. Later
ORT sessions also disable telemetry. Optional weights always load offline; remote model code is not executed.

## Outfit artifact boundary

The single-centroid paired profile, `character-lock-ccip-outfit-v3`, requires a validated artifact and explicit
CCIP/FashionCLIP directories. Its single-view candidates failed independent controls and remain unavailable.
Missing/invalid explicit configuration never silently falls back to an easier scorer.

The experimental relationship model uses two learned cosine features, CCIP and FashionCLIP, after a fixed
visible-subject/lower-65%/512px-white-containment transform. The frozen balanced logistic recipe uses L2 0.001.
It yields a learned outfit-match score, not a raw cosine, identity probability or universal clothing guarantee.
FashionCLIP source: [official model repository](https://huggingface.co/patrickjohncyh/fashion-clip), MIT-tagged,
revision `7e3ba62ce16b379a1ab479346b66f192e76f51b7`. No FashionCLIP weights are bundled.

An artifact binds both feature-model weights, FashionCLIP config/processor checksums, preprocessing version,
dataset/frozen-fit checksums, disjoint training/held-out identities and image hashes. Its held-out pair predictions
and at least six independent same/different-outfit controls must reproduce at the unchanged 0.90 threshold.
Runtime readiness refuses failed, incomplete, overlapping or inconsistent proofs. The QA-only matcher can
evaluate controls before the final artifact exists; worker configuration exposes no validation bypass.

If a future single-centroid artifact meets that contract, its method is `ccip-outfit-pair-v3:<artifact SHA256>`. The worker retains
raw feature measurements, identity method and feature-model methods in job history. The server rejects missing,
non-finite or mismatched lineage/features. The compound method includes the artifact's model/preprocessing
lineage; old evidence is retired when a new method produces current evidence.

## Validated matching against kept outfit views

`MV_OUTFIT_MODE=multiview`, with the explicit CCIP/FashionCLIP directories and its validated artifact, selects
`character-lock-ccip-outfit-multiview-v1`. It evaluates the **unchanged frozen pair model** against each kept
outfit view separately and takes the maximum learned match; pose views are not averaged into a synthetic pose.
The full-image identity cosine must first pass 0.90. Different identities have no outfit judgment; a matching
identity with outfit references must have a measured outfit result. The outfit threshold stays 0.90.
Raw per-view CCIP cosines remain inspectable features, not a second outfit gate. This learned score is not a
probability. Raw nearest-view CCIP and an extra raw-CCIP gate both failed prospective controls and were rejected.

Migration **0203** adds nullable `character_refs.outfit_id`. Keeping an outfit reference through C3 binds the
current default variant under the existing character lock/revision fence. Existing unassigned views are not
guessed into a named variant. Scoring uses that variant's main image followed by only its bound views, deduplicated
and capped at five. Generation also excludes another variant's outfit views. Switching the default never
reassigns old views. Bound variant IDs participate in the evidence fingerprint; legacy empty IDs preserve the
old fingerprint. SQLite and PostgreSQL cover binding, switching and stale keeps.

The distinct `mangaveyra.outfit-multiview.v1` artifact reproduces the frozen pair fit's disjoint held-out proof,
five independent canonical reference hashes and six fresh controls. Both correct-costume positives must pass
identity/outfit; two different people in the uniform must fail identity; two changed costumes must fail clothing.
At least one changed costume must pass identity, proving the clothing rejection is independently required.
Candidate images cannot overlap fitting, held-out or canonical reference images. The tunic control also lowers
identity confidence to 0.891585; that recognition limitation remains recorded rather than regenerated away.
The five canonical views were generated and visually reviewed separately; no chapter/control image became a
reference or fitting input. Fit parameters, threshold, transforms and reference pack were frozen before this set.

The checked-in [example artifact](../../worker/examples/scoring/outfit-multiview-v1.json) has SHA256
`e88fd3f73d56fad19bd35daa8acb907003ce4acd65ee8370027b7c2849566008`.
The [training-only freeze](../../worker/examples/scoring/outfit-pair-frozen-fit.json) preserves the original
parameters. FashionCLIP weights SHA256 is `4977e3a54929eccf065ce449aeaf296f0e5cb6b28e8798c3c97d67cb2f6dafc9`;
config/processor hashes and raw held-out/control features are in the artifact. Weights remain externally
provisioned and offline, with their original terms. SigLIP alternatives were evaluated locally and rejected;
they are not production dependencies or an implicit fallback.

The method is `ccip-outfit-multiview-v1:<artifact SHA256>`. Each worker result retains per-view CCIP/FashionCLIP
features, fitted parameters, predictions, selected indices and feature-model methods. The server reproduces all
predictions and their maximum, validates dimensions/finite bounds/lineage and enforces the identity gate before
saving. All batches must return the same artifact-bound method. Canonical references and assessment changed
none of the thirty page documents or image assets. The 30-page final identity/outfit results both pass.

## Quality limit and rollback

The independent study covers three broad original wardrobe families. It cannot establish performance on unseen
styles, subtle costume changes or arbitrary body types. The failed single-view/SigLIP/raw-CCIP candidates were
not applied to the acceptance chapter or enabled in its preview. Their failures remain in the plans and evidence.
The validated per-view profile passes the thirty distinct original-manga appearances; its limited validation is
not a universal clothing or identity guarantee. Human visual review remains necessary.
See [the continuation QA record](../qa/phase-2-completion-2026-11-08.md).

Scoring is read-only with respect to artwork. Repairs remain explicit human actions with the existing scopes,
locks, version fences and spend reservations. Rollback drains jobs, removes optional paired configuration and
reassesses with the selected CCIP/DINOv2 profile. Immutable job history and the original failed results remain.
