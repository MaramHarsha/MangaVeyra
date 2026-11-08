# Phase 2 live acceptance

Status: complete under owner approval on 2026-11-08; final consistency and scope/pixel acceptance pass.
The initial failed DINOv2 result below is retained as historical evidence.
Parent: [plan 0006](0006-phase-2-consistency-and-control.md), milestone 5.

The software checks use isolated fixtures. Final acceptance additionally needs a real 30-page original chapter,
learned identity/outfit measurements, and an isolated page-7 hand correction. Synthetic scores and duplicated
artwork cannot establish this acceptance.

## Proposed bounded run

- Use the owner's saved direct OpenAI connection, through MangaVeyra's generation service and reservation ledger.
  Only OpenAI is in scope. Never print or persist a decrypted provider key.
- Use a new temporary project/data folder, preserving the source database, account, keys and manuscripts.
- Provision an isolated temporary Python environment with PyTorch, Transformers and safetensors, and cache only
  `facebook/dinov2-small` weights plus their processor/config files. Record the model revision/checksum.
  Runtime scoring loads that explicit directory offline; no runtime automatic downloads or remote model code.
- Cap total additional OpenAI spend at USD 10, including references, 30 character appearances, repair and the
  page-7 masked correction. Use one candidate per deliberately dispatched call. Stop before a reservation would
  cross the remaining cap. Do not switch providers or raise limits automatically.
- Keep original structured panels, cast, independent art layers and editable lettering. Use varied script beats
  and poses; do not repeat one generated image across the chapter to manufacture consistency.
- Record per-appearance identity and outfit similarities with the scoring method and reference fingerprint.
  Inspect actual manga output. The product's 0.90 drift threshold is a policy threshold, not a calibrated identity
  probability. Report failures and unassessed appearances; never replace missing scores with structural/mock values.
- Compare page-7 pixels outside the selected mask exactly, and all unrelated page documents/assets byte for byte.
  Exercise compare/revert against the actual version store. No publishing, sharing, deleting or unlocking.

## Approval basis

`AGENTS.md` says: “Do not silently install dependencies or contact external providers without permission.”
The existing worker environment has numpy/Pillow/OpenCV/pytest; PyTorch, Transformers and DINOv2 weights are absent.
Earlier live authorization covered the separate Phase 1 check. This run is a new, explicitly bounded chapter-scale
assessment. The owner explicitly approved the isolated model setup and USD 10 test. The cap covers the entire additional run.

## Sources and limits

[DINOv2 model card](https://github.com/facebookresearch/dinov2/blob/main/MODEL_CARD.md),
[Small checkpoint](https://huggingface.co/facebook/dinov2-small),
[Transformers DINOv2 API](https://huggingface.co/docs/transformers/model_doc/dinov2).
DINOv2 Small is a general visual feature model; its similarity alone does not prove drawn-character identity or
outfit correctness. Live results and human visual review are separate evidence from protocol tests.

## Recorded result

The run used 39 direct OpenAI image calls and USD **2.124936**, with no pending reservations and no other provider.
It generated an original kept sheet, 30 separate character appearances, three intentionally recurring scenery
plates and one ambient FX layer, then repaired pages 1/7/17 and made the selected page-7 hand correction.
Structured lettering remained editable. The hand correction changed 4,393 selected pixels; all 441,616 pixels
outside its 84×76 mask were identical. The other 29 canonical page documents, 33 unrelated immutable assets and
the rest of page 7 stayed unchanged. Actual before/after history snapshots exist; browser restore also passed.

Final current-reference assessment covers all 30 appearances and outfits, with no unassessed results. The mean of
the per-appearance lower identity/outfit scores is **0.6765267**. Identity ranges 0.4117–0.8710; outfit ranges
0.3588–0.8761. **All 30 remain below 0.90.** Representative repairs did not satisfy the threshold. The output is
black-and-white manga, but these results do not establish the roadmap's consistency acceptance.

Weights: revision `ed25f3a31f01632728cabb09d1542f84ab7b0056`, SHA256
`ae1e99fcefd534ed978cdeb8326f08030c96e28b7a81ffcbc98a857c84d14be1`.
Environment, reproducible harness, evidence locations, design comparison and remaining work are in
[the QA record](../qa/phase-2-control-2026-11-08.md). Further model selection/calibration must preserve honest
evidence and the owner’s acceptance contract; this failed result must not be described as Phase 2 completion.

## Authorized continuation

The owner requested completion and delegated the scorer/setup choice after the explicit request to evaluate CCIP
and install the isolated CPU test dependencies. The same USD 10 total cap remains in force. CCIP is a separately
provisioned, opt-in anime-character scorer with OpenRAIL-tagged weights; DINOv2 remains available. No weights are
bundled and no runtime download is enabled. Independent original identity/outfit controls are generated in a
separate temporary project, rather than adding acceptance-page images to the kept references.

The first CCIP run followed by 13 deliberate D12 layer corrections brought all 30 identity measurements above
0.90. Ten outfit measurements remained below 0.90; combined acceptance was still open at that checkpoint. A labelled page-7
region run retains the original evidence and again proves all 29 other documents and every outside pixel unchanged.
Current evidence and the additional model comparisons are recorded in the continuation QA record.

The initial bounded wardrobe experiments concluded without a validated replacement: the framing-corrected candidate
passed fresh held-out identities but failed one of six fresh controls, so it is not enabled or applied to the
chapter. Full-precision CCIP reassessment retains 30 identity passes, 20 combined passes and ten outfit failures.
At that checkpoint the total was 113 OpenAI calls / USD 5.926425 of the original USD 10 cap, with no pending reservations.
See [the continuation QA record](../qa/phase-2-completion-2026-11-08.md) and
[plan 0008](0008-outfit-validation.md) for those preserved failures.

The reference-coverage continuation in [plan 0009](0009-outfit-reference-coverage.md) subsequently validated the
unchanged frozen clothing fit against each of five dedicated kept outfit views. The final fresh controls produce
all intended pass/reject decisions, with component limitations explicitly recorded. No acceptance image enters
fitting or the reference pack, and the 0.90 identity/outfit gates remain unchanged. Four reviewed pose views were
kept only after validation. The final real worker/server assessment passes all thirty identity/outfit checks;
all page documents and image assets stay unchanged. The page-7 corrected asset remains current, with 4,399 changed
inside pixels, all 441,616 outside pixels identical and the other twenty-nine page documents/assets unchanged.
Final total: **141 OpenAI calls / USD 7.278736 / 10**, zero pending reservations. All applicable Go, web, worker,
browser, edition, template/module and desktop/basic-phone checks pass; mobile optimisation remains deferred.
