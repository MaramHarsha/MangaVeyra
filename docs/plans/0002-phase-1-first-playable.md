# Phase 1: First playable — premise → exported chapter

- Status: Active
- Supersedes the scope of plan 0001 (its contracts for text generation, the script document, the script service,
  provider secrets and spend reservations are kept and are part of this plan).
- Decisions: ADR 0001 (scope), 0002 (embedder), 0003 (spend reservations), 0004 (design addendum).

## Handoff status (2026-11-07)

- **Done (commit "Phase 1 foundation"):** `internal/assets` (implemented, tested), `internal/pagedoc` (document,
  validation, geometry, 12 layouts; tested), every contract file listed below, the `pagestore` module stub registered
  in `internal/app/modules.go`, embedded lettering fonts in `internal/lettering/fonts/` (OFL), and the
  `github.com/tdewolff/canvas` dependency (pure-Go vector drawing with WOFF2 fonts, vertical Japanese via
  `RichText.SetWritingMode(canvas.VerticalRL)`, PNG and PDF renderers — spike verified).
- **Not started:** Waves 1–3 below.
- **Approved contract addition, not yet in `internal/studio/contract.go`:** add
  `NewProject{Title, Premise, Logline, Style, Format, PageSize, Language string; Genres, Tags []string; PageWidth,
  PageHeight int; SourceText string}`, `NewCharacter{ProjectID, Name, Kanji, Role, RoleLine, Age, Look, Personality,
  SpeechStyle, Category string}` and the Service methods `CreateProject(ctx, actor, NewProject) (Project, error)`
  (owner membership in the same tx), `CreateChapter(ctx, actor, projectID, title) (Chapter, error)`,
  `CreateCharacter(ctx, actor, NewCharacter) (Character, error)` and `SourceText(ctx, projectID) (string, error)`.
  The demo seed and the end-to-end test need them.
- **Review rule from PR #5:** every data-layer test runs on both dialects through a `forEach` helper over
  `testdb.Dialects()` with `testutil.WithDialect` (see `internal/jobs/jobs_test.go`), so PostgreSQL is exercised
  whenever `MV_TEST_POSTGRES_URL` is set.
- **Design references** live outside the repository in `MangaVeyra_All_Pages/` (16 `.dc.html` canvases, 184 screens,
  `10 Prototype.dc.html`); screen IDs map to routes in `docs/SCREENS.md`.

## Outcome

On a laptop with no GPU, a creator who connected only an OpenRouter key (or nothing: the built-in placeholder
provider) can:

1. create a project with the new-project wizard (premise → genre → style → format → generate), or adapt existing text;
2. get a chapter script (scenes, panel-by-panel camera, emotion, action, dialogue, SFX) and edit it;
3. create characters with generated or uploaded reference sheets;
4. turn the script into laid-out pages with panels, generate panel art (4 variations, keep one), edit one region,
   and letter the page (auto-placed bubbles; right-to-left order; vertical Japanese);
5. export the chapter as PNG ZIP, CBZ or print-ready PDF (B5 or KDP trims, bleed, optional credits page).

Out of scope here: LoRA training UI and drift fixing (Phase 2), publishing and the reader (Phase 3), translation and
animation (Phase 5), World (Phase 7). The XP/rank layer stays deferred.

## Shared contracts (root-owned, frozen)

| Package | Contract | Consumers |
|---|---|---|
| `internal/assets` | `*assets.Service` — store files, rows, signed URLs (implemented) | everyone |
| `internal/pagedoc` | `pagedoc.Page` document, validation, geometry, built-in layouts (implemented) | pagestore, editor, gen, lettering, compose, export |
| `internal/ai/contract.go` | `ai.TextService`, `ai.ImageService`, errors, task names | gen, setup |
| `internal/studio/contract.go` | `studio.Service`, roles/actions, script document | gen, editor, export, pagestore |
| `internal/pagestore/contract.go` | `pagestore.Service` — page persistence, locks, versions | editor, gen, export |
| `internal/lettering/contract.go` | `lettering.Engine` — layout, draw, auto-place, reading order | editor, compose, gen |
| `internal/compose/contract.go` | `compose.Renderer` — page → canvas/PNG | export, editor (thumbnails), gen |
| `internal/export/contract.go` | `export.Service`, trims, `export.chapter` job | editor (E2) |
| `internal/gen/contract.go` | `gen.Service` — enqueue generation; job types | studio screens, editor |

Changing a contract requires updating this file first. Services are registered with `core.Provide` in the
providing module's `New` and looked up with `core.MustUse` in `Init` or handlers.

## Waves and ownership

Each agent works in its own git worktree and branch, edits only its paths, and adds new per-module locale files
(`internal/i18n/locales/{en,ja}/<module>.json`) and, if needed, one new stylesheet `web/src/styles/<module>.css`
plus one `@import` line in `web/src/styles/index.css`. Root owns `internal/core/**`, `internal/app/**`,
`internal/assets`, `internal/pagedoc`, all contract files, `go.mod/go.sum` (agents may add a dependency and report
it), migrations other than those assigned, and integration.

### Wave 1 (parallel)

- **AI** — `internal/ai/**`, migration `0200_spend_reservations.sql`: model catalogue (embedded manifests),
  provider registry with sealed keys, adapters (placeholder text+image, OpenRouter text+image, fal image,
  OpenAI-compatible text e.g. Ollama, local worker image via child worker jobs), router with modes, local-only,
  licence guard, explanations and fallback, hard spend reservations (ADR 0003), G2 Models & providers and a minimal
  provider-connect flow, and the setup wizard's model step (`setup.ModelStep`).
- **Studio** — `internal/studio/**`: projects (+ owner membership), chapters, characters (refs, outfits, uploads),
  setting items, script service, `CheckScopeWritable` validator, screens B1 (claim `/studio`), B2, B3 wizard
  (premise or adapt text), C1 story editor, C2/C3/C4 characters, C5 Setting.
- **Page pipeline** — `internal/pagestore/**`, `internal/lettering/**`, `internal/compose/**`, `internal/export/**`:
  page persistence with locks and snapshots, lettering engine with embedded fonts, compositor, PNG/PDF/CBZ export
  with trims and credits page, the `export.chapter` job and the E2 export screen route.

### Wave 2 (parallel, after Wave 1 merges)

- **Generation** — `internal/gen/**`: script, character sheet, pages-from-script (layout suggestion, prompts from the
  script, auto-lettering), panel art with variations, region edit; candidates and apply.
- **Editor** — `internal/editor/**`, `web/src/islands/editor/**`: page JSON API over pagestore + lettering layouts,
  the D1 editor island (PixiJS canvas, filmstrip, layers, properties, change scope), D2 layout picker, D8
  variations, D13 region edit (mask painting), D3 lettering mode, undo/redo, shortcuts.

### Wave 3 (root)

Integration, `seed --demo` (Tidebreak Courier), Playwright end-to-end flow on the placeholder provider, README
status, full verification, PR.

## Acceptance criteria

- Fresh install, no keys: wizard → script → characters → pages → art (placeholder) → bubbles → PDF export works.
- With an OpenRouter key: the same flow uses real text and image models; costs are estimated, reserved and reconciled;
  local-only blocks every remote call (counting-transport tests).
- Provider keys are sealed with AAD `provider:<id>:key`, never returned or logged.
- Locks are enforced server-side for every AI, batch and save path.
- Bubbles are never rasterised into layer art; export draws them from the same layout the editor shows.
- `go build ./... && go vet ./... && go test ./...`, `templ generate` with no drift, `npm test` and `npm run build`
  in `web/`, and worker `pytest` are green.

## Risks

- Remote provider APIs change: adapters keep request/response mapping in one file each with recorded fixtures.
- Image models vary in reference support: the router filters on manifest capabilities; placeholder is the oracle.
- PixiJS bundle size: the editor island loads only on editor routes.
