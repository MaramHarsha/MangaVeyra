# Execute the remaining Phase 1 gates

- Status: Complete — first playable, desktop comparison and all final gates verified (2026-11-07)
- Branch: `phase-1/first-playable`; starting commit `a8d52ff`.
- Normative scope and interfaces: plan 0002, plan 0001 for Gate A, ADRs 0001–0007.
- Outcome: a fresh install with no keys completes premise → editable script → characters → laid-out pages →
  independently editable art → structured bubbles → PNG ZIP / CBZ / print-ready PDF.
- Deliberately outside this work: publishing/reader, World, training/drift repair, animation, translation and
  unfinished mobile artboards. Their designed controls remain visible in the shared disabled Soon state.

## Decisions and dependencies

The approved desktop handoff is readable at `../MangaVeyra_All_Pages/`; every touched screen follows its artboard.
The existing tokens and UI kit own appearance. Necessary undrawn states are recorded in ADR 0004 §8. Gate A and
its reviewed authorization, secrets, spend and revision guarantees remain regression gates. New shared additions
are recorded in plan 0002 before implementation. Existing normalized page/character schema and its JSON fields
are reused; root alone owns any migration proven necessary by tests.

Remote image/local endpoint adapters are tested with fake transports only. Published official provider documents
freeze each remote model, endpoint, input/output ceilings and reservation bound in an ADR before integration.
The two owner-reserved decisions remain open for the PR: ADR 0005's estimate charge for unsent/refused calls and
the deprecated OpenRouter text model. No live paid-provider calls are authorized.

## Milestones and ownership

Agents use separate temporary git worktrees and branches, with non-overlapping ownership. Each returns logical
commits, exact tests, and remaining risks; root reviews and cherry-picks into the existing Phase 1 branch.

1. [x] Root readiness: transactional Studio helpers, staged assets, additive frozen interfaces, validation tests.
2. [x] Gate B AI agent: `internal/ai/**` except shared contracts, module-local locales/styles; catalogue, image
   adapters, local text/worker jobs, modes/licence/fallback, G11/G12 and faithful G2 updates. Root imports styles,
   merges locale entry points and owns provider ADRs/dependencies.
3. [x] Gate B Studio agent: `internal/studio/**` except root-owned service/lock/contracts; `internal/studioui/**`
   except root integration to C1; new `studiobible` locale/styles. B2/C2/C3/C4/C5 and refs/outfits/uploads/setting.
4. [x] Gate B rendering agent: `internal/lettering/**`, `internal/compose/**`, `internal/export/**` except contracts;
   new module-local locale/styles. Shared glyph geometry, polygon compositing, exports/job and E2 design.
5. [x] Root Gate B page store: `internal/pagestore/**`, all shared contracts, assets, pagedoc, migrations, app/core,
   generated integration, dependency manifests and shared locale entry points. Versions/revert/locks and parity tests.

6. [x] Gate C generation: assign `internal/gen/**` except contracts after Gate B interfaces pass. Implement
   character.sheet/pages.from_script/panel.generate/region.edit/candidates/apply with lease/revision/lock fencing.
7. [x] Gate C editor: assign `internal/editor/**`, `web/src/islands/editor/**` and editor-local locale/styles after
   Gate B interfaces pass. D1/D2/D3/D8/D13, filmstrip/layers/properties/scope, undo/redo/shortcuts and mask painting.
8. [x] Root final integration: C1 Make pages action, routes/navlink coverage, original Tidebreak Courier demo seed,
   Playwright `e2e/`, README and design comparison evidence.
9. [x] Full verification and independent read-only review. Delivery uses logical commits, branch push and one PR
   titled `Phase 1: first playable` against `main`. Do not merge.

Integrated evidence (2026-11-07): full Go build/vet/tests and race suites pass on SQLite and PostgreSQL 18.
Independent review reproduced and fixed retained PDF raster memory, hidden lettering, lost continuity anchors,
Japanese preview language, chapter provenance and the manuscript/completed-job read race. Worker component slots,
parent-attempt fencing, budgets and observed-cost reconciliation pass fake-worker regressions. The complete browser
flow and the desktop comparison are finished; required later-phase controls remain Soon.

Final-review corrections: chapter art/lettering labels now aggregate all stored chapter content in the mutation
transaction, including human edits, deletions and reverts; script attribution is preserved. Locked lettering cannot
create unsavable browser edits. D8 deep links restore the variations tray. C4 removes the finished-job skeleton once
candidates arrive. E2 uses an authorized per-job download endpoint so archive filenames keep their `.cbz`/`.zip`
extensions rather than the generic media object's name. Desktop D2 and D13 now use the designed modal and bounded
before/after candidate workspace, with controls for later-phase tools visibly Soon.

Final verification record:

- `gofmt -l cmd internal` and `git diff --check`: empty; `go build ./...`, `go vet ./...`, `go test ./...`: pass.
- Full `go test -race ./...` on SQLite and `MV_TEST_POSTGRES_URL=... go test -race -p 4 ./...` with a throwaway
  `postgres:18-alpine`: pass. The latter runs all dialect-aware cases on both databases.
- `templ generate` and `go mod tidy`: no generated/dependency drift.
- `go test -count=1 ./internal/editioncheck/...`, bilingual catalogue validation and the built-page navigation test:
  pass. Navigation includes all four custom-provider steps and every new editor state.
- Frontend `npm run check`, `npm test` (51 tests), `npm run build`: pass. Strict UI audit: zero findings.
- Existing worker environment, `python -m pytest -q`: 153 passed, 2 skipped because optional torch/WebSocket
  backends are absent. No dependencies were installed for final verification.
- Playwright using installed Chrome: 1 complete flow passed (38.2 s), including masked candidates, lettering,
  conflicts/locks, correctly named PDF/CBZ/PNG ZIP downloads, all four local-provider steps against a loopback fake,
  JavaScript-disabled screens/proof, and all 13 phone routes with no overflow and 44px enabled-control targets.
- Actual `mangaveyra seed --demo --owner demo-check` CLI on a throwaway install, run twice: one original project,
  two chapters, five characters, four pages, zero jobs and zero published series after each run.
- All desktop references and matching captures inspected at 1440×900; responsive editor inspected at 390×844.
  Comparison notes below record the dynamic data and required option/state differences.

## Desktop comparison notes

References are extracted from the approved desktop artboards using `e2e/scripts/capture-artboards.mjs`; the
application captures use the same 1440×900 viewport, local fonts/icons and production CSP. Final screenshots disable
finite CSS animations so the comparison cannot capture a half-faded dialog. The source handoff stays outside Git.

| Screen | Implemented design and necessary runtime differences |
|---|---|
| B2 Projects | Approved project grid, filters and view controls; actual projects/counts. The explicit filter submit supports JavaScript-disabled use. Import remains Soon. |
| B3 / C1 | Preserve Gate A's five-step wizard and three-pane manuscript. Pages-from-script is live. Deferred outlines/assistant/continuity controls remain Soon as already recorded. |
| C2 Character bible | Three-column profile-card grid, approved role/name hierarchy and real reference images. No invented training/consistency scores. |
| C3 Character detail | Portrait/reference/expression strips, profile facts, outfits and designed relationship area. Training, voice and relationship operations remain Soon. |
| C4 Create character | Approved two-column brief and four-candidate grid; upload/profile details and explicit Keep use the existing form kit. Training claims are replaced with Soon. |
| C5 Setting | Approved location/prop/style-reference grid and tabs; original saved content or the documented empty state replaces sample counts and usage claims. |
| G2 Models | Four provider tiles and capability table; actual installed/connected model catalogue. Model IDs are available as titles without adding a second table line. |
| G11 Routing | Approved decisions/pins/guards columns. Real routing history or the documented empty state; numeric own-key spend guard and explicit Save use existing fields. |
| G12 Custom provider | Full four-step connection/test/description/routing flow. Implemented OpenAI-compatible local text uses a non-generation model-list test; future connection types remain Soon. |
| D1 Editor | Approved workbench columns, filmstrip, layers, scope/properties, canvas context toolbar and footer; real job/connection state replaces fictitious GPU/collaborator data. |
| D2 Layout picker | Approved centered outlined modal, suggested/all/saved tabs, layout cards and tilt toggle; unsafe destructive layout choices are disabled. Saved/custom layouts remain Soon. |
| D3 Lettering | Approved bubble-type/speaker/text/font/size/order inspector and shared shaped outlines. Geometry, vertical Japanese and ruby fields extend the existing inspector patterns. |
| D8 Variations | Four real previews, selected-candidate proof, scoped rerolls and explicit Keep. No fabricated consistency scores; global queue navigation remains Soon. |
| D13 Region edit | Brush/lasso/box, feather control, before/after comparison and four-candidate strip; natural layer proportions are retained. Object selection/Character Lock/style controls remain Soon. |
| E2 Export | Approved modal over the editor proof; CBZ and required DPI/trim/range fields extend its existing option grid. Actual page counts/output bounds replace sample file/time estimates. EPUB/video/loops remain Soon. |

Responsive checks use the desktop DOM at 390×844; unfinished mobile artboards remain out of scope. The browser suite
checks overflow and 44px enabled-control targets, and verifies the server-rendered pages/editor proof without JS.

## Verification and acceptance

Every data-layer behavior uses `testdb.Dialects()` or its `forEach` wrapper with fresh databases. Meaningful new
behavior includes regressions for authorization, asset ancestry, invalid input, stale revisions/leases (including
manual retry), locks, Local only, provider errors, spend reconciliation, and successful persistence/output.

Required commands and runtime checks:

Build Vite assets before starting the final Go gates, then freeze `internal/web/static/dist/` until Go tests and
browser verification finish. Rebuilding Vite during a long Go run can remove hashed files after `embed` records
their paths, producing a build failure unrelated to test assertions. Final verification uses stable built assets.

- `gofmt -l cmd internal` (empty), `go build ./...`, `go vet ./...`, SQLite `go test ./...`.
- `go test -race ./...` on SQLite and with `MV_TEST_POSTGRES_URL` against a throwaway `postgres:18-alpine`.
- `templ generate` with no generated drift; `go mod tidy` with no unintended dependency drift.
- `go test -count=1 ./internal/editioncheck/...` and `TestBuiltPagesHaveNoDeadLinks` including every new route.
- `npm run check`, `npm test`, `npm run build` in `web/`; `python3 -m pytest -q` in `worker/`.
- Playwright fresh-install placeholder flow through PDF/CBZ, and failures/locks/conflicts/locale checks.
- 1440×900 app/artboard side-by-side for B2/C2/C3/C4/C5/G2/G11/G12/D1/D2/D3/D8/D13/E2, with remaining
  differences and reasons recorded for the PR. At 390×844 no overflow, readable text, 44px targets and usable nav.
- Strict premium audit is local evidence only; never stage or commit `premium-audit.json`.

## Risks and recovery

Database writes must never hold storage/network I/O inside a lease-fence callback. Stage objects first and insert
their rows inside the fence; discard uncommitted objects after rollback. Parent-child worker jobs retain immutable
parent attempt identity. Mutable scopes recheck authorization, project ancestry, version and locks inside the same
transaction as persistence. Region edits restore every pixel outside the mask and never change lettering art.

Page saves preserve locked descendants, snapshots exclude signed URLs, and reverting creates a new version rather
than rewriting history. PDF dimensions, trim/bleed boxes, page order, Unicode glyphs/ruby and archive contents need
structural tests as well as rendered inspection. Temporary DBs, worktrees and browser data stay separate from the
owner's current app data and provider keys. Logical commits are the rollback boundary; this task never merges.
