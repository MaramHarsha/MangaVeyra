# Phase 2: Consistency and control

- Status: Active — milestone 1 complete; milestones 2–5 remain.
- Base: merged Phase 1, `12041ed` on `main`.
- Branch: `phase-2/consistency-control`.
- Owner direction: start Phase 2, follow the supplied design exactly; mobile UI is deferred.
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
- User data, provider keys and live preview databases are not migrated or copied for Phase 2 development.

## Milestones and dependencies

1. [x] **Queue and notifications (G9/G4).** Build the complete designed surfaces over the existing services. Bounded,
   URL-restorable lists; real progress/worker facts; safe per-job state/actions; per-user filters and explicit read
   actions. No prompts, input/output payloads, secrets or raw upstream errors in queue markup. Preserve server-side
   authority, ordinary no-JavaScript forms, safe links and recovery. Activate shell links only after routes exist.
2. [ ] **History and scoped control.** Compare/revert through the existing version fence; page regeneration with
   neighbour context; explicit page/panel locks; prove unrelated pages/layers/lettering stay unchanged.
3. [ ] **Character consistency.** Reference-first identity/outfit assessment and drift indicators in C3/C4/D8;
   designed D12 fix scoped to the character layer. Select and document an appropriately licensed scoring backend
   before claiming measured identity scores. Optional worker LoRA training retains honest readiness and provenance.
4. [ ] **Pose and sketch (D4).** Designed pose presets/stick sketch and sketch-to-manga with bounded inputs, explicit
   scope and protected lettering/locks. No invented quality score or unsupported backend promise.
5. [ ] **Integrated Phase 2 verification.** Design comparison, local gates and a recorded chapter-scale quality
   assessment using configured capabilities. Mark the phase complete only when its final acceptance has evidence.

Milestone 1 does not depend on GPU quality or a new provider. Later milestones depend on their service contracts
and backend capability being demonstrated; a disabled Soon control is not an implemented feature.
Milestone 1 results and remaining design/runtime differences are recorded in
[the activity QA record](../qa/phase-2-activity-2026-11-07.md).

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
