# Phase 2 outfit checker validation

Status: single-view/SigLIP experiments concluded and rejected. Reference-coverage continuation completed in
[plan 0009](0009-outfit-reference-coverage.md); its distinct per-view profile passes final acceptance in
[plan 0006](0006-phase-2-consistency-and-control.md). Preserve the failures below.

At this experiment's starting checkpoint all 30 identities passed with explicitly provisioned CCIP, while ten
lower-body outfit cosine measurements failed.
Neither a larger CCIP variant nor FashionCLIP's raw cosine meets the existing validation bar. Do not lower the
0.90 threshold or call those measurements probabilities. Keep the original results and all protected artwork.

## Experiment and acceptance

Build an independent, entirely original wardrobe dataset in a separate temporary project: three broad original
outfits, three different identities per outfit, and four separately generated poses per identity (neutral, seated,
wide step and crouched/folded clothing). Two identities
per outfit are training data; the third is held out. Reference and candidate images in the acceptance chapter,
the six earlier benchmark controls, and their scores must never enter training or parameter selection.

Freeze a small relationship classifier over two learned measurements: CCIP and FashionCLIP lower-body cosine.
It has no character names, garment labels, image IDs, acceptance-page data or raw image pixels as inputs. Use a
balanced logistic objective with fixed L2 regularization, and the existing 0.90 score threshold. Record the exact
feature definitions, model checksums, split, fitted parameters, training recipe and held-out predictions.

First validate the new generated images visually and test the frozen classifier against the held-out identities
and earlier independent controls. Do not ship it or change the chapter's displayed evidence unless the held-out
checks pass. Never adjust parameters after inspecting the acceptance chapter. A failure remains a failure; do not
fabricate confidence or hide a failed raw measurement. If adopted, name it as a learned outfit-match score,
retain distinct identity/outfit evidence, and use a new attributable method ID that invalidates old percentages.

The final acceptance still requires all 30 current appearances above the unchanged policy threshold and the
page-7 pixel/document isolation proof. Optional model files are explicitly provisioned, offline and licensed;
there are no downloads or telemetry during worker startup/inference.

## Bounded framing correction

The first fixed fit passed its nine held-out positives (minimum 0.93805) and 24 negatives (maximum 0.24101),
but failed three of the six earlier controls. It is rejected and never advertised by the runtime. Preserve its
artifact, source and predictions separately. The acceptance chapter was not evaluated with it.

The canonical front is 240×715; generated layers are 560×800. FashionCLIP's centre crop therefore discards
different garment regions. Test one fixed correction: locate the visible subject on white, take its lower 65%,
and contain that complete region in a white 512px square before either feature model. Keep the same two features,
balanced objective, L2 0.001, training identities and 0.90 policy; freeze this recipe before fitting. Give it a new
preprocessing/schema/method version. No acceptance images or scores enter fitting.

Generate three fresh original identities (one per outfit, four poses each) and six fresh controls. The earlier
held-out set/controls are development evidence after this change; the fresh sets are the final validation.
Require every fresh same-outfit example ≥0.90 and every changed outfit <0.90, before evaluating the chapter.
Do not tune this second candidate against either fresh set or the chapter. If it fails, keep the quality gate open.

The frozen second fit passed all fresh held-out pairs (nine positives, minimum 0.985322; 24 negatives, maximum
0.043813), but only five of six fresh controls. It rejected the seated man in the matching courier uniform
(0.048277). It was not evaluated on the acceptance chapter, enabled in the preview, or given a passing control
proof. Runtime readiness requires both reproducible held-out and independent-control proofs and therefore refuses
this candidate. The first rejected candidate and all original raw measurements remain preserved.

At this experiment checkpoint: 113 direct OpenAI image calls, USD 5.926425 of the original USD 10 cap, no pending
reservations. Raw CCIP evidence retained all thirty identity passes and ten outfit failures. Later reference-view
validation and the distinct adopted method are recorded in plan 0009 and the completion QA, not relabelled here.

## Identity-first profile (prospective validation)

The failed cross-identity uniform control demonstrates that the classifier cannot promise costume comparison
across different people. Character Lock's actual scope is one named character. A new v3 profile therefore first
verifies identity at 0.90 and emits no outfit score when identity fails; identity drift still flags the appearance.
When identity passes and an outfit reference exists, a real outfit score remains mandatory at the same 0.90
threshold. Do not silently hide a failing outfit for a matching identity.

Keep the v2 feature transform, training-only frozen parameters and rejected evidence unchanged. Use a separate
schema/method with an explicit identity gate, and generate six fresh controls before evaluating acceptance pages:
two matching-character/matching-outfit poses, two different characters in matching uniforms, and two matching
characters in changed outfits. Require both matching positives to pass identity and outfit; changed outfits must
fail the raw frozen outfit checker; different identities must fail identity and have no runtime outfit result.
Retain their raw research scores without claiming a cross-identity outfit judgment. The prior v2 failure remains
a failure and cannot be relabelled as a passing global clothing model. Only after this new scoped validation
passes may v3 assess the unchanged chapter. Any remaining matching-identity outfit failure remains a failure.

The v3 controls rejected a matching Mina kneeling behind a map (identity 0.950410, outfit 0.515216). That candidate
is also rejected and was never applied to the chapter. Scope gating alone does not resolve garment occlusion.

## Clothing-feature replacement evaluation

Evaluate the official Apache-2.0-tagged `Marqo/marqo-fashionSigLIP` vision ONNX export, revision
`c56244cc94f92419e8369fa71efdaf403b124ce8`, as a replacement clothing feature. Read numeric preprocessing locally;
never execute its `auto_map` repository code. The already installed CPU ONNX dependencies suffice. Keep the same
body framing, CCIP identity gate, original 24 training images, fixed balanced L2 0.001 recipe and 0.90 policy.
Freeze parameters before evaluating all existing controls as development data. Do not inspect chapter results.

Only if development supports this replacement, generate a fresh disjoint three-identity/four-pose set and six
fresh controls, including held objects/occlusion. Require all scoped controls and held-out checks before adoption
or chapter evaluation. Neither earlier failed candidate nor an earlier development control becomes fresh proof.
The same USD 10 total cap applies, and no new paid generation is needed for the development evaluation.

The SigLIP lower-body-only development also fails the occluded-map positive. Before another evaluation, correct
the inherited region mismatch: retain the existing CCIP lower-body feature, but give the fashion encoder the
complete visible subject contained in a white square, preserving the upper garment and the full input it was
trained to compare. This is a separately frozen preprocessing candidate; keep the prior failure and parameters.
The same original training set/recipe, identity gate and fresh-validation requirement apply. Same-identity changed
outfits must still fail, so retaining the face cannot substitute for actual clothing discrimination.

## Ownership, budget and verification

The root owns the QA harness/dataset, worker scorer/config/tests, narrow generation-method wiring, bilingual
method labels and evidence docs. Existing desktop artboards/components are unchanged; mobile optimisation stays
deferred. No concurrent editing agents. The original USD 10 total OpenAI cap covers this dataset as well.

Run focused worker/model-boundary and SQLite/PostgreSQL protocol checks before integration; then the required
Go, frontend, worker, template/module-drift, edition and browser gates from plan 0006. The CPU fixtures prove
training and protocol behavior, not full-size GPU art quality. Keep private media, credentials and session state
outside Git. A failed experiment can be rolled back without changing the original manuscript or score history.
