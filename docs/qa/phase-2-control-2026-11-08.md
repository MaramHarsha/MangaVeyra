# Phase 2 control and live acceptance verification

Date: 2026-11-08. Branch: `phase-2/consistency-control`, continuation after `1fc67ce`.
Contracts: [plan 0006](../plans/0006-phase-2-consistency-and-control.md),
[approved live run](../plans/0007-phase-2-live-acceptance.md),
[ADR 0010](../decisions/0010-phase-2-scoped-control-and-character-evidence.md).

## Outcome

The designed control flows are implemented and their software checks pass. **Phase 2 is not complete:**
the real 30-page chapter fails the existing 0.90 consistency threshold. No quality percentage was fabricated,
no threshold was lowered and no repeated character image was used to manufacture a pass.

The live check generated original black-and-white manga using the saved direct OpenAI connection. The original
account/manuscripts/configuration remain in their source data folder; only the required account/sealed provider
and local encryption key were copied into the isolated acceptance folder. No publishing, sharing, deletion,
unlocking or GitHub delivery was performed.

## Implemented and inspected behavior

- D10 compares actual immutable snapshots and restores through the version fence. Snapshot reads reject another
  project/chapter/page. Branch creation/merge remain Soon. The latest 50 snapshots remain visible.
- Page regeneration reads immediate neighbours for background continuity, stages every art component and commits
  once per page. Failures, cancellation, late locks and changed neighbours cannot partially replace a page.
- D12 repairs only explicitly selected character layers. Active duplicate coordinators are reused and conflicting
  overlapping operations are refused. Locked targets remain unavailable. Before/after evidence and current drift
  refresh without replacing protected art or text.
- C3/C4 show current-reference learned evidence, absent/outfit-pending states, real queue progress and optional
  compatible-worker training. Old percentages, structural/mock scoring and stale bible evidence are excluded.
  Training requires distinct references and rejects mock, malformed, overlapping or unpaired safetensors. Actual
  GPU training quality has not been exercised here.
- Pose presets and the joint editor persist bounded normalized coordinates, with pointer and keyboard input.
  D4 accepts bounded uploads/drawings, keeps existing polygons/cast/lettering and uses the selected composition
  strength/finish. Camera and automatic sketch detection remain honest Soon states. Local unsupported pose
  control fails explicitly. Native reference guides retain all supported references.
- Region-mask callbacks cannot revive a discarded selection. Navigation cannot race an in-flight variation apply.
  Workbench requests are bounded/aborted when obsolete; an unsaved sketch uses the existing navigation guard.

## Desktop design comparison

Opened the actual D4/D10/D12 desktop artboards in file 09 and the C3/C4 artboards in file 07. Runtime captures
were compared at 1440×900, using the existing palette, fonts, icons, UI kit and responsive patterns.

| Surface | Match and necessary runtime states |
|---|---|
| D4 | Full 1100 px stage/340 px inspector, 52 px bar, two 340×480 proofs and arrow; Source, strength, Detect and Finish sections; bottom route and 40 px action. Upload/drawing and pending/result/error states replace sample art. Detect rows describe saved content; Camera/detection show Soon. |
| D12 | Full stage/inspector, two 380×300 proofs, arrow, warning outline, references, selected drift rows, What changes, route and action. Actual names/counts/evidence replace samples. Missing results do not display the artboard's example 97%. Selection/lock/quote helpers are documented necessary states. |
| D10 | Two 400 px proofs near the top, selected-snapshot accent outline, compact history rows, branch section and paired footer actions. One real `main` branch replaces example branches; branch actions remain Soon. UTC/action/version metadata replaces sample author/change strings. |
| C3/C4 | Existing Character Lock card and reference-creation flow. Real evidence/readiness/progress replace mock percentages and invented training durations. Low scores use the existing warning token. |

A late-loading editor stylesheet initially restored the ordinary three-column grid in the workbenches. The
variant selector now takes precedence; browser geometry checks prove stage x=0 and inspector width=340.
Pose thumbnails occupy their designed card area without flex shrinking to an unreadable size. Close controls follow the existing inspector pattern; redundant header shortcuts are hidden. See
[design addendum §10](../decisions/0004-design-addendum.md).

Desktop artboards remain the source of truth. Basic 390×844 use is checked; mobile artboards/optimisation are
deferred. Japanese copy still needs native review before a public release.

## Software verification

| Gate | Result |
|---|---|
| `go vet ./...` | Pass |
| Full `go test -count=1 ./...` with isolated PostgreSQL 18 enabled | Pass; 37 tested packages, including SQLite/PostgreSQL data paths |
| Focused generation/editor/bible/edition checks | Pass |
| `gofmt`, whitespace gate | Pass after formatting the queue job-label switch |
| `templ generate`, `go mod tidy`, hash comparison | No generated/module drift; generated templ sources retained |
| Frontend check/tests/build | Pass; 110 tests |
| Worker pytest | 157 passed, 2 optional integrations skipped (GPU diffusers environment and ComfyUI websocket fixture) |
| Strict premium audit | Zero findings; includes editor islands and shared styles |
| Focused browser flow | Pass: creation, script, references, art/region apply, lettering/locks/conflicts, pose preset and keyboard joint save, visual history/restore, page generation, sketch input/result, current drift/repair and exports; all three workbenches additionally pass Japanese/Paper/reduced-motion and 390×844 checks; existing queue/notification flows also pass |

The GPU integration skip belongs to the existing unit-test environment. Actual DINOv2 inference was separately
run in the explicitly approved temporary environment and exercised through the real worker/server protocol.
No optional diffusion or training weights were downloaded. Existing demo-island initial-state and HTMX dependency
`eval` build warnings remain; runtime HTMX eval remains disabled under CSP. No missing local tool remains unresolved;
the existing templ binary was invoked from `/Users/harsha/go/bin/templ`.

## Real provider and scoring acceptance

Original chapter: **Tideglass Relay — The Last Tide Gate**, with original character **Mina Sazanami**. Script and
art retain AI provenance. Thirty single-panel pages have 30 different generated character assets and editable
dialogue; three recurring harbour scenery plates and one ambient overlay are explicitly intentional reuse.
The full contact sheet was visually inspected as black-and-white manga, with varied actions/poses.

| Check | Recorded result |
|---|---|
| Remote route | Saved OpenAI connection, `gpt-image-2.5-sunburst`; 39 one-candidate calls including references, scenery/FX, three representative repairs and one masked correction |
| Additional spend | **USD 2.124936** of the approved USD 10; no pending reservation, no other provider |
| Learned method | DINOv2 Small, `dinov2-small-v1:<SHA256>`, CPU inference; all 30 current-reference identity and outfit measurements present |
| Mean conservative similarity | **0.6765267**, using the lower identity/outfit score per appearance |
| Identity / outfit ranges | 0.4117–0.8710 / 0.3588–0.8761 |
| Threshold acceptance | **Fail: 30/30 below 0.90**, including after real D12 repairs on pages 1, 7 and 17 |
| Page-7 hand correction | 84×76 mask; **4,393** pixels changed inside, **441,616** outside pixels identical |
| Protected work | Other **29** canonical page documents, **33** unrelated asset checksums, remaining page-7 content and all lettering unchanged |
| Actual history | Before/after snapshots match the canonical documents; restore separately passes in the browser flow |

DINOv2 revision: `ed25f3a31f01632728cabb09d1542f84ab7b0056`; weights SHA256:
`ae1e99fcefd534ed978cdeb8326f08030c96e28b7a81ffcbc98a857c84d14be1`.
Temporary setup used Python 3.12, torch 2.15.0, transformers 5.22.2 and safetensors 0.8.0. The model is loaded
offline from its explicit directory; no runtime repository code or implicit weight download is permitted.

Whole-sheet vs single-body comparison was investigated before the chapter run. Individual kept front/side/back
crops are preferred when present, with the original AI provenance retained; the generation route still uses the
kept sheet first. Diagnostic head/torso/letterbox comparisons did not establish a threshold pass and were not used
to inflate recorded scores. The current general visual metric remains uncalibrated for manga identity/outfits.
The next acceptance work must improve/validate measured consistency with independent examples and a suitable
licensed scorer, rather than relabel this failed run.

## Reproduction and local evidence

`cmd/phase2check` is gated by `MV_PHASE2_LIVE_APPROVED=1`, a hard USD 10 cap, one saved OpenAI route and a newly
created temporary data directory. It reuses recorded job IDs on resume. Stages are `init`, `references`, `curate`,
`chapter`, `plates`, `repair`, `region`, `assess`, `report` and `serve`. `init` needs the source folder and required
account/provider IDs; pass locations/IDs locally, never a plaintext key. Scoring stages require explicit
`--python`, `--dino` and `--worker` paths. Curation and hand-mask coordinates must come from inspecting the actual
generated images; they are not assumed from another run.

Local evidence is under `/private/tmp/mv-phase2-control-evidence/` (artboard/browser captures, gate logs, audit and
`live-30-pages.png`) and `/private/tmp/mv-phase2-live-acceptance/` (`report.json`, `region-result.json`, immutable
before/after character PNGs, chapter proofs and private resumable state). Scoring dependencies and weights are in
separate temporary directories. Sealed credentials, session state, generated private media and raw runtime data
are not committed. Reproducing live calls requires explicit approval; software fixtures remain offline.

Final captures of the actual live chapter are in `live-ui/`: `D1-page7-desktop.png`, `C3-desktop.png`,
`D12-desktop.png` and `D10-desktop.png`, all at 1440×900. The isolated preview remains available at
`http://127.0.0.1:64021` while its local process runs; it uses the temporary acceptance database.

Rollback: drain/cancel new jobs through the queue before using an older binary. Migration 0202 is additive;
older binaries can leave its evidence table in place. Original source data was not migrated or altered.
