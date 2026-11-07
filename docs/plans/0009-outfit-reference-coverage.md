# Phase 2: explicit outfit reference views

Status: complete continuation of plan 0006. The validated per-view clothing profile passes all thirty chapter
identity/outfit checks; failed single-view, SigLIP, raw-CCIP and extra-gate candidates remain rejected.

Character Lock compares one named character and its expected outfit. A single front reference does not cover
folded garments across poses. Test multiple explicitly kept pose views of the same default outfit with the
existing CCIP model, using a named multiview method and unchanged 0.90 policy. This is reference coverage, not
training on the acceptance chapter or converting failed raw scores into probabilities.

## Original experiment contract (refined by the recorded validation below)

1. Generate four dedicated canonical Mina outfit views (seated, crouched, wide step, raised hands), conditioned
   only on the original kept references. Visually inspect them. They are reference material, never test controls.
2. Evaluate against all prior controls as development data, with identity verified first. Use fixed full-subject
   bounding/white containment for the lower-65% outfit region and maximum reference cosine. No chapter evaluation.
3. Only if development supports it, generate six fresh controls: two matching poses with occlusion/folded clothing,
   two different people in the uniform and two matching-identity changed outfits, including close costume cases.
4. Outfit views must be bound to the actual outfit variant, not pooled across unrelated costumes. Preserve the
   existing C3 reference-upload layout; keeping an outfit reference binds the current default under the character
   revision fence. Legacy unbound references remain visible. The root owns a portable nullable relationship column,
   Studio reads/writes, scoring/fingerprints, worker mode/protocol and bilingual existing-form helper copy.
5. Enable the explicit multiview profile only after independent validation. Preserve raw cosines and selected
   reference evidence in job history. Different identities get no outfit judgment. Matching identities must have
   real outfit evidence when references exist. Model/reference changes retire prior measurements.
6. Assess the 30 unchanged chapter appearances. Any remaining failure stays visible. Scoped D12 corrections, if
   needed, preserve all other documents/layers/text; a changed page-7 asset requires a fresh masked-hand proof.

The root owns integration and all files; no concurrent agents. Use existing tokens/components and desktop
artboards. No new layout, mobile-artboard implementation, automatic downloads or telemetry. Reuse the approved
offline CCIP model; all OpenAI activity shares the original USD 10 total cap. Source user data stays untouched.

Verify relationship/default switching and reference/method retirement on SQLite/PostgreSQL; malformed/gated worker
results; worker model/input checks; then the applicable Go, web, worker, edition, template/module, audit and browser
gates from plan 0006. Compare 1440×900 and basic 390×844 usability. Record actual quality and scope evidence.

Risks: reference count can bias nearest-view scores; cap views at five and validate costume counterexamples.
Do not keep chapter/test images as references. Variant binding prevents accidentally accepting another costume.
Rollback selects the original scorer and retains new references/history without deleting or unlocking content.

## Conservative pose/clothing agreement

Raw nearest-view CCIP passed the eighteen development controls but falsely accepted the fresh dark zip jacket
(0.958807). It is not enabled as the authoritative outfit checker. Matching poses passed; the white trench
counterexample was correctly rejected. Preserve that failed result.

Require a second, independent clothing comparison: evaluate the already frozen FashionCLIP/CCIP pair model
against each of the same five kept views, take its maximum match score, then take the **minimum** of that score
and the raw CCIP pose match. Identity, raw pose match and learned clothing match must each pass 0.90. No fit
parameters, thresholds or references change. Retain all per-view cosines/predictions and the frozen model lineage.

All 24 existing controls are now development data. The frozen original FashionCLIP pair model with these fixed
views passes all 24; so does the SigLIP body variant. Prefer the already implemented FashionCLIP model to avoid
an additional runtime model family. SigLIP's full-subject variant still fails and remains rejected.

Before chapter evaluation, generate six completely new controls with changed occluders/poses, different people
in the uniform, and further dark/light costume counterexamples. Validate the exact conservative combination and
only then keep the dedicated canonical views/assess the book. A failure stays a failure; do not soften any gate.

The prospective agreement controls rejected both different costumes and both different people, but the extra
raw CCIP gate falsely rejected the correctly dressed letter-holding side view (0.847607). The frozen clothing
pair score was 0.999765. This conservative agreement candidate is rejected, not applied to the chapter.

Use the original reference-coverage approach directly with the already frozen clothing relationship model:
evaluate each kept reference/candidate pair independently and take the maximum learned match. This model
already uses both CCIP and FashionCLIP features; adding an unrelated raw CCIP outfit threshold proved harmful.
Retain raw per-view cosines for inspection, but require the fixed 0.90 identity and learned-outfit gates. No
refitting, reference changes, threshold changes, acceptance-image fitting or probability claims. Freeze this
aggregation before generating a final prospective set (two matching poses, two different people, two further
dark/light costumes). The preceding thirty controls become development data. Enable only after that fresh set.

Final prospective results: both matching poses pass identity/outfit; both different people fail identity;
both changed costumes fail the learned clothing check. The light-tunic identity also falls to 0.891585; preserve
that recognition false alarm instead of regenerating a lucky example. The dark-blazer identity passes 0.919224
but clothing fails 0.444599, proving clothing rejection is independently necessary. Fix the artifact validator's
overly restrictive negative precondition: a deliberately changed costume need not pass identity, but its raw
clothing measurement must still reject it, and at least one hard costume counterexample must pass identity.
Runtime thresholds, predictions, reference pack, feature transforms, fit parameters and aggregation are unchanged.
This is six correct intended acceptance/rejection decisions with the recorded component limitation, not perfect
identity recognition across arbitrary costumes. Visually reviewed all six as original black-and-white manga.

## Recorded completion

Kept the four dedicated canonical views through the normal Bible service only after prospective validation,
bound to the existing Tide Courier variant. Main outfit plus four poses gives five distinct reference images;
the original three identity views and every identity measurement remain unchanged. All thirty canonical page
documents and character asset IDs remain unchanged. Final worker/server scoring passes all thirty identity and
outfit gates with method `ccip-outfit-multiview-v1`, artifact SHA256
`e88fd3f73d56fad19bd35daa8acb907003ce4acd65ee8370027b7c2849566008`.
The learned clothing score range is 0.9624792060–0.9999822114; identity remains 0.9002536268–0.9608192337.
No further D12 correction was required and the current page-7 hand proof remains intact.

Go vet/full Go with SQLite/PostgreSQL, focused binding/protocol/retirement checks, 112 web tests/check/build,
214 worker tests (zero skipped), two browser flows, edition, template/module drift and strict design audit pass.
Desktop and basic 390×844 evidence are refreshed; mobile optimisation remains deferred. The sole OpenAI ledger
ends at 141 calls / USD 7.278736 / 10 with zero pending reservations. See the
[completion record](../qa/phase-2-completion-2026-11-08.md) for limits, artifacts, comparison notes and rollback.
