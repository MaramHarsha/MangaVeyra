# Phase 2: Consistency and control

- Status: Active — milestones 1, 2 and 4 implemented and verified; learned-consistency acceptance in 3/5 has failed.
- Base: merged Phase 1, `12041ed` on `main`.
- Branch: `phase-2/consistency-control`.
- Owner direction: start Phase 2, follow the supplied design exactly; mobile UI is deferred.
- Continuation: the owner requested completion of the remaining milestones on 2026-11-08.
- Scope source: `../MangaVeyra-build-status-and-phases.md`, Phase 2; feature contracts in `docs/FEATURES.md`.

## Outcome and boundaries

Phase 2 makes character consistency and correction controllable without disturbing approved work. Its final
acceptance remains the roadmap's 30-page consistency assessment and an isolated page-7 region correction. This
plan does not treat those quality claims as proven by deterministic mocks or by one live image.

Use the approved desktop artboards exclusively, existing runtime tokens and `internal/web/ui`. Keep the full
designed screens and disabled Soon controls for capabilities not yet implemented. Publishing/reader/community,
mobile optimisation, translation, animation, World and any new visual direction are outside this phase.

## Existing runtime and decisions

- Phase 1 already provides independent art/lettering layers, layer/region jobs, scope and lock guards, exact
  outside-mask restoration, page versions/revert, routing modes/pins/licence guards and kept character references.
- `internal/jobs` owns queue state, fair sharing, leases, cancellation, safe public views, manual retry and worker
  administration. Existing APIs restrict lists to the owner, with an explicit admin-wide view; bearer scope and
  project restrictions remain unchanged. Browser queue actions require a session.
- `internal/notify` owns per-user notification persistence, preferences, delivery and read state. Existing job
  completion senders already link to `/queue?job=...`. No new outbound delivery is configured by this work.
- Screen sources: G9 in `09 Screens - Flow 6-7 Tools and System.dc.html`, G4 in
  `08 Screens - Flow 4-5 Publish Read Translate.dc.html`. Layout, copy, hierarchy, iconography and navigation must
  match these artboards at 1440×900. Phones use existing responsive patterns; no mobile-artboard implementation.
- Development fixtures are isolated. The owner separately approved a bounded live run on 2026-11-08: only the
  required account and sealed OpenAI connection were copied into a new temporary database, with the matching
  encryption key kept locally at rest. The source database was opened read-only; no manuscripts were copied.

## Milestones and dependencies

1. [x] **Queue and notifications (G9/G4).** Build the complete designed surfaces over the existing services. Bounded,
   URL-restorable lists; real progress/worker facts; safe per-job state/actions; per-user filters and explicit read
   actions. No prompts, input/output payloads, secrets or raw upstream errors in queue markup. Preserve server-side
   authority, ordinary no-JavaScript forms, safe links and recovery. Activate shell links only after routes exist.
2. [x] **History and scoped control.** Compare/revert through the existing version fence; page regeneration with
   neighbour context; explicit page/panel locks; prove unrelated pages/layers/lettering stay unchanged.
3. [ ] **Character consistency.** Reference-first identity/outfit assessment and drift indicators in C3/C4/D8;
   designed D12 fix scoped to the character layer. Select and document an appropriately licensed scoring backend
   before claiming measured identity scores. Optional worker LoRA training retains honest readiness and provenance.
4. [x] **Pose and sketch (D4).** Designed pose presets/stick sketch and sketch-to-manga with bounded inputs, explicit
   scope and protected lettering/locks. No invented quality score or unsupported backend promise.
5. [ ] **Integrated Phase 2 verification.** Design comparison, local gates and a recorded chapter-scale quality
   assessment using configured capabilities. Mark the phase complete only when its final acceptance has evidence.

Milestone 1 does not depend on GPU quality or a new provider. Later milestones depend on their service contracts
and backend capability being demonstrated; a disabled Soon control is not an implemented feature.
Milestone 1 results and remaining design/runtime differences are recorded in
[the activity QA record](../qa/phase-2-activity-2026-11-07.md).

The remaining control implementation, verification and live acceptance results are recorded in
[the control QA record](../qa/phase-2-control-2026-11-08.md) and
[ADR 0010](../decisions/0010-phase-2-scoped-control-and-character-evidence.md). Character assessment/repair and
optional training have runtime paths and protocol checks; the final consistency requirement is still unsatisfied.
The real chapter has 30 different generated character assets. All 30 fall below 0.90 with the current learned
metric, including after representative repairs. Do not mark Phase 2 complete, lower the threshold to make this
test pass, substitute synthetic scores or repeat one character image across pages.

## Remaining implementation contract

- Single-page regeneration stages all independent art layers and commits the page atomically. It reads only the
  immediately preceding/following pages for context; it never writes them. All target locks, authorization,
  neighbour revisions and target versions are checked at enqueue, dispatch and commit. Lettering stays structured.
- Basic history uses D10's two-preview/history anatomy. Branch creation/merge remain visible Soon (Phase 6).
  Snapshot access is scoped to the current project/chapter/page. Restoring creates a new version.
- Character Lock uses explicit reference-first evaluation, separate identity/outfit evidence and a named scoring
  method. DINOv2 is the selected optional learned embedder; no restricted InsightFace weights are selected
  automatically and no structural/mock result is presented as a measured identity percentage.
- D12 repairs only explicitly selected character layers. D4 keeps the existing polygon layout and editable
  lettering, with upload/drawing and a composition-strength control. Unsupported detection/camera capabilities
  retain the designed disabled controls with an honest reason.
- Optional worker training must bind the character/reference revision, compatible base model and artifact. It must
  never report ready on a mock artifact or install/download weights automatically.
- The root owns `internal/{gen,editor,pagedoc,pagestore,studio,studioui,ai}`, worker scoring/training integration,
  editor islands/styles, bilingual catalogues, migrations, E2E and evidence. No concurrent editing agents.
- Final quality assessment requires a real configured scoring backend and original chapter art. Deterministic
  tests prove scope isolation and recovery, not the roadmap's 30-page visual-quality acceptance. Prepare the
  assessment and report any missing runtime capability explicitly; do not mark that acceptance complete without it.

## Ownership and verification

The root owns all implementation, shared wiring/contracts, catalogues, templates and generated files. Current file
scope: `internal/jobs`, `internal/notify`, `internal/activityui` (browser adapter avoiding the foundation import cycle),
`internal/app/{modules,app}.go` and notification regression coverage, shared menu/readiness/chrome, `web/src/styles`, the queue refresh utility and the D8 queue
link, bilingual activity catalogues, UX/audit contracts, `e2e` and evidence records.
Do not introduce a feature-package import of `internal/app` or a cycle through `core`/`web`.

Commands: focused queue/notification/UI tests; `gofmt` check; `go vet ./...`; `go test -count=1 ./...`; the same data
tests against an isolated PostgreSQL database; `templ generate` plus drift check; `go mod tidy` plus module drift
check; `go test -count=1 ./internal/editioncheck/...`; web `npm run check`, `npm test`, `npm run build`; strict premium
audit. Worker pytest applies only if worker code changes. Browser flow covers signed-out/unauthorized, populated,
empty, filtered, paginated, success/error, no-JavaScript, keyboard, English/Japanese, both themes and reduced motion.
Compare G9/G4 side by side with the 1440×900 artboards; verify basic 390×844 usability without a mobile redesign.

## Risks and rollback

Queue metadata and notifications can disclose private work: user ownership/admin authority and escaped, safe links
are enforced at every read/mutation. Retrying may dispatch real work: preserve the existing retry policy, spend
reservations and lock/revision fences; no automatic retries are added by the screen. Stale forms must not mutate a
different job or notification. Read state changes require an explicit POST and must never mark another user's data.
No deletion, sharing, publishing, unlocking, provider calls, dependency installation or GitHub delivery is needed
for milestone 1. Rollback is the isolated Phase 2 branch; existing databases and original app data remain intact.
