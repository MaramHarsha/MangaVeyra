# Phase 2 completion evidence

Date: 2026-11-08. Base: `cb19ea3`, branch `phase-2/consistency-control`.
Parent: [plan 0006](../plans/0006-phase-2-consistency-and-control.md).
Earlier evidence: [activity QA](phase-2-activity-2026-11-07.md), [initial control QA](phase-2-control-2026-11-08.md).

## Outcome

**Phase 2 acceptance passed.** All thirty distinct original-manga character appearances pass both identity and
outfit checks at the unchanged 0.90 threshold. The selected hand correction changes only its masked pixels;
all other page documents, protected fields, layers and structured lettering remain unchanged. The approved
source database/manuscripts were opened read-only for connection setup and were never migrated or edited.

The final explicit offline method is `ccip-outfit-multiview-v1`, bound to artifact SHA256
`e88fd3f73d56fad19bd35daa8acb907003ce4acd65ee8370027b7c2849566008`. Identity remains full-image CCIP cosine;
outfit is a separately named learned clothing match against kept outfit pose views. It is not a raw cosine or
an identity probability. Human creative review remains necessary for unfamiliar styles, costumes and body types.
Original failed results remain separate; none were overwritten into passing values.

| Final acceptance | Observed result |
| --- | --- |
| Character appearances | 30 distinct assets, 30 assessed, zero drift, zero unassessed |
| Identity range | 0.9002536268–0.9608192337; unchanged from the preceding raw CCIP assessment |
| Learned outfit-match range | 0.9624792060–0.9999822114 |
| Mean of the lower identity/outfit score | 0.9370438592 |
| Reference keep and final assessment | All 30 page documents and all character image assets unchanged |
| Page-7 hand | 4,399 changed inside pixels; 441,616 outside pixels identical |
| Hand scope/history | Other 29 canonical page documents and unrelated asset SHA256s unchanged; real before/after snapshots match; current page 7 still uses that corrected asset |
| Total direct OpenAI use | 141 calls, USD **7.278736 / 10**, zero pending reservations; no other provider |

The chapter is **Tideglass Relay — The Last Tide Gate**, with Mina Sazanami. It uses thirty separate one-panel
character appearances, three deliberately recurring background plates, an ambient FX layer and editable structured
speech. No repeated character raster, acceptance-image training or synthetic score establishes this result.

## Completed runtime corrections

- Explicit offline CCIP alongside DINOv2; methods are bound to requested profiles and frozen across assessment
  batches. Model/weight/reference changes retire old C3 aggregates, twelve-bar chart and D8/page evidence.
- Separate identity/outfit evidence, identity-first clothing scope and server reproduction of every per-view
  clothing prediction. Current-reference matching, dimensions, finite values, feature lineage, selected indices,
  fixed fit and aggregation are checked before saving. Matching identities cannot omit available outfit evidence.
- Migration **0203** binds kept outfit views to their actual default outfit under the existing character revision
  fence. Scoring/generation exclude other variants; switching defaults never reassigns views. Legacy unbound
  views remain visible without silently entering a named variant. SQLite/PostgreSQL cover these contracts.
- Full measurement precision is preserved before policy decisions. English/Japanese percentages cannot round a
  failing value, including the adjacent floating-point value below 0.90, into a visible pass.
- LoRA preparation keeps the complete character, composites alpha onto white and does not silently mirror
  asymmetric identity details. Built-in and external dataset preparation share the same behavior.
- Sketch conversion suppresses a saved pose before its first planning call; ordinary panel generation still
  requires pose support. Protected metadata, neighbours and lettering remain unchanged.
- ONNX Runtime and Hugging Face telemetry gates run before optional imports, including ORT initialization.
  Explicit local weights load offline with remote repository code disabled. An earlier initialization event file
  is retained privately as diagnostic evidence; it is not represented as never having occurred.
- Temporary QA workers get unique process names and are drained on exit, without resuming another worker's pause.

## Model validation and preserved failures

The owner approved isolated DINOv2 setup and one USD 10 total OpenAI cap, then delegated the CCIP/CPU setup choice.
CCIP uses the official OpenRAIL-tagged graph, FashionCLIP the official MIT-tagged weights; pinned revisions,
checksums, protocol and provisioning instructions are in [ADR 0011](../decisions/0011-explicit-anime-scoring-and-outfit-validation.md).
The feature weights are not in Git and no runtime download is enabled.

The clothing fit uses twenty-four original images from six training identities and a fixed two-feature balanced
logistic recipe (L2 0.001). Its independent twelve-image/three-identity validation contains nine positive pairs
(minimum 0.985322) and twenty-four negatives (maximum 0.043813). The original frozen parameters are retained.
No chapter or control image was used for fitting. The score is a learned match, not a calibrated probability.

| Earlier candidate | Preserved outcome |
| --- | --- |
| Original DINOv2 | All thirty below 0.90; mean lower score 0.6765267 |
| Raw CCIP after thirteen scoped D12 corrections | All thirty identities pass; twenty outfits pass, ten fail; mean lower score 0.8956290306 |
| Single-view clothing fit, before framing correction | Held-out pairs pass; three earlier matching-outfit controls fail |
| Single-view fit after full-subject/lower-65%/white-512 framing | Fresh held-out pairs pass; seated man in the uniform rejected at 0.048277 |
| Identity-first single-view fit | Matching Mina behind a map rejected at 0.515216 |
| SigLIP alternatives | Occlusion/folded-pose positives fail; not production dependencies |
| Raw nearest-view CCIP | Changed dark jacket falsely accepted at 0.958807 |
| Extra raw-CCIP gate plus the clothing check | Correct letter-holding pose falsely rejected at 0.847607; clothing score was 0.999765 |

These failed candidates were not applied to the acceptance chapter after failing controls. Their sources,
predictions and failed measurements remain recorded in [plan 0008](../plans/0008-outfit-validation.md) and
[plan 0009](../plans/0009-outfit-reference-coverage.md).

The adopted profile compares the unchanged frozen clothing fit to each of five dedicated canonical outfit views
individually, taking the maximum learned match. Four new poses were generated from the original references,
visually reviewed and frozen before final controls. They are reference material, never held-out controls.
The outfit transform, fit, 0.90 gates and five-reference pack did not change after prospective evaluation.

The final fresh six original controls produce all intended acceptance/rejection decisions: both matching poses
pass identity/outfit, both different people in the uniform fail identity, and both changed costumes fail clothing.
The hard dark-blazer negative passes identity 0.919224 but fails clothing 0.444599. The light tunic fails clothing
0.709155 and also lowers identity to 0.891585; this recognition false alarm remains explicitly recorded. The
validator was corrected to allow a changed costume to fail identity as well, while requiring an independent
identity-passing clothing negative. No measurement, model parameter or runtime threshold was changed.

Only after this validation were the four dedicated pose images copied to owned assets and kept on the main QA
character through the normal Bible service, bound to **Tide Courier**. All thirty page documents stayed unchanged.
The final assessment uses the real worker/server path; the server reproduces the per-view predictions. Identity
measurements and all thirty asset IDs exactly match the preceding raw CCIP report. The limited wardrobe sample
cannot establish arbitrary clothing-detail, style or body-type recognition accuracy.

## Software and design verification

- `gofmt`/whitespace, `go vet ./...`, full `go test -count=1 ./...`: pass. SQLite and isolated PostgreSQL 18 enabled.
- Focused reference binding/default switching, stale keeps, protocol/lineage, model/reference retirement and
  sketch-composition regressions pass. Explicit edition/locale gate passes.
- Frontend check, **112 tests**, production build: pass. Existing HTMX and demo-island warnings remain.
- Full worker suite: **214 passed, zero skipped**. Actual CPU generation/edit/inpaint, LoRA training/use,
  ControlNet dispatch and ComfyUI websocket progress/previews are exercised.
- The tiny diffusion checkpoint is pinned at `fa06b5c3d6d45fabd978486616c6dae068519e85`, converted locally with
  `torch.load(weights_only=True)` to safetensors. The ControlNet fixture uses random weights and the tiny VAE
  scale; it proves dispatch/tensor compatibility, not full-size GPU pose or training quality.
- Browser: **2 passed (3.8 minutes)**. Full premise-to-export flow covers D4/D10/D12, keyboard pose editing,
  repair/history/revert, Japanese/Paper/reduced motion and 390×844 usability. The second flow checks all ten
  content languages in both English/Japanese interfaces and their exports.
- Strict premium audit: zero findings. Template regeneration: zero drift. Offline module tidy: no unintended
  `go.mod`/`go.sum` change; package lock unchanged.
- Final actual-data captures: D1/page 7, C3, D12 and D10 at **1440×900**. D1/C3/D12 also checked at **390×844**;
  document width equals viewport width (390). C3 has exactly twelve actual current-method bars, 36px high.

The approved desktop handoff remains the visual source. No new tokens, fonts, icons, navigation or mobile-artboard
layout were introduced. C3's actual longer name wraps; eight references, measurement caption and necessary
assessment/training/readiness explanations make its card taller than the handoff's populated sample. The grid,
components and hierarchy are retained. Its responsive action row wraps and the existing end-aligned training
tooltip no longer overflows the page. D12 contains complete kept thumbnails and uses the recorded empty state
because there is no drift; there is no invented after score. Its repair/history behavior is separately exercised
with drift fixtures. Deferred destinations and unfinished later-phase features remain visible, disabled Soon.
These data/state differences are the remaining comparison notes for any later PR description. Mobile optimisation
is deferred by the owner. State additions are in [addendum 0004](../decisions/0004-design-addendum.md).

## Reproduction and review locations

The validated artifact and its training-only freeze are checked in under `worker/examples/scoring/`; loading them
reproduces the held-out/control predictions without contacting a service. The worker README describes explicit
provisioning. Private images, session state, sealed provider/account material, keys, tokens and feature-model
weights are excluded from Git.

Use `cmd/phase2check` with the existing approved isolated data root and durable job IDs. `benchmark-outfit-multiview`
generates the final fresh controls; `keep-outfit-views --reviewed-outfit-views --outfit-model ...` deliberately keeps
reviewed canonical references. `assess` / `serve` use `--python`, `--ccip`, `--fashion`, `--outfit-model` and
`--outfit-mode multiview`. Keep the original USD 10 cap; never rerun setup into a new ledger to reset it.
`repair-drift --round 1` and `region --region-label final-ccip` retain earlier scoped repair/hand evidence.
No additional manuscript regeneration was needed after the validated references were kept.

Raw state: `/private/tmp/mv-phase2-live-acceptance/`. Private evidence: `/private/tmp/mv-phase2-control-evidence/`.
Key files are `report-initial-dino.json`, `report-ccip-before-multiview.json`, final `report.json`,
`final-ccip-region-result.json`, `outfit-reference-keep-result.json`, `outfit-multiview-frozen-policy.json`,
`outfit-multiview-fresh-validation.json`, `final-acceptance-summary.json`, gate logs and desktop/phone captures.
A local review preview runs at `http://127.0.0.1:64021`. Review copies of the screenshots are under the Orca chat
scratch folder's `phase-2-live-review/`. Temporary test/model folders may be removed by the operating system.

Rollback drains current jobs, removes the optional outfit-view profile and explicitly reassesses with the chosen
CCIP/DINOv2 model. Retain additive reference relationships, original failed reports and immutable job history.
This completion was not pushed, merged or published; those actions require the owner's explicit instruction.
