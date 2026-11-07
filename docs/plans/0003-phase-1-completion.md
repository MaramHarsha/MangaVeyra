# Execute the remaining Phase 1 gates

- Status: Active — Gate B implementation
- Branch: `phase-1/first-playable`; starting commit `a8d52ff`.
- Normative scope and interfaces: plan 0002, plan 0001 for Gate A, ADRs 0001–0006.
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
2. [ ] Gate B AI agent: `internal/ai/**` except shared contracts, module-local locales/styles; catalogue, image
   adapters, local text/worker jobs, modes/licence/fallback, G11/G12 and faithful G2 updates. Root imports styles,
   merges locale entry points and owns provider ADRs/dependencies.
3. [ ] Gate B Studio agent: `internal/studio/**` except root-owned service/lock/contracts; `internal/studioui/**`
   except root integration to C1; new `studiobible` locale/styles. B2/C2/C3/C4/C5 and refs/outfits/uploads/setting.
4. [ ] Gate B rendering agent: `internal/lettering/**`, `internal/compose/**`, `internal/export/**` except contracts;
   new module-local locale/styles. Shared glyph geometry, polygon compositing, exports/job and E2 design.
5. [x] Root Gate B page store: `internal/pagestore/**`, all shared contracts, assets, pagedoc, migrations, app/core,
   generated integration, dependency manifests and shared locale entry points. Versions/revert/locks and parity tests.

Root prerequisite/page-store evidence: full Go suite passed on SQLite and PostgreSQL18 after integration. The
store's regression cases exercise normalized pixel geometry/ruby, URL-free snapshots, stale/concurrent saves,
foreign assets and membership, locked descendants, atomic replacement rollback, versions/revert, reorder/delete,
and human-only destructive/unlock actions. Rendering, generation and final browser evidence remain pending.
6. [ ] Gate C generation: assign `internal/gen/**` except contracts after Gate B interfaces pass. Implement
   character.sheet/pages.from_script/panel.generate/region.edit/candidates/apply with lease/revision/lock fencing.
7. [ ] Gate C editor: assign `internal/editor/**`, `web/src/islands/editor/**` and editor-local locale/styles after
   Gate B interfaces pass. D1/D2/D3/D8/D13, filmstrip/layers/properties/scope, undo/redo/shortcuts and mask painting.
8. [ ] Root final integration: C1 Make pages action, routes/navlink coverage, original Tidebreak Courier demo seed,
   Playwright `e2e/`, README and design comparison evidence.
9. [ ] Full verification and independent read-only review, followed by logical commits, branch push and one PR
   titled `Phase 1: first playable` against `main`. Do not merge.

## Verification and acceptance

Every data-layer behavior uses `testdb.Dialects()` or its `forEach` wrapper with fresh databases. Meaningful new
behavior includes regressions for authorization, asset ancestry, invalid input, stale revisions/leases (including
manual retry), locks, Local only, provider errors, spend reconciliation, and successful persistence/output.

Required commands and runtime checks:

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
