# Phase 1: First playable — premise → exported chapter

- Status: Active — Gate A complete; Gate B is next
- Gate A: plan 0001 is the mandatory first vertical slice and remains normative for structured text generation,
  project/chapter/script persistence, authorization, provider secrets, spend reservations and atomic job completion.
  Gate A is green as of 2026-11-07; page-pipeline and visual-generation work may now begin under Gate B.
- Decisions: ADR 0001 (scope), 0002 (embedder), 0003 (spend reservations), 0004 (design addendum),
  0005 (Gate A OpenRouter text boundary), 0006 (OpenAI direct text route; strict-schema fix for both routes).
- Remaining execution: `docs/plans/0003-phase-1-completion.md` tracks Gates B/C, desktop design evidence,
  final verification and the authorized commits/push/pull request.

## Handoff status (2026-11-07)

- **Done (commit "Phase 1 foundation"):** `internal/assets` (implemented, tested), `internal/pagedoc` (document,
  validation, geometry, 12 layouts; tested), every contract file listed below, the `pagestore` module stub registered
  in `internal/app/modules.go`, embedded lettering fonts in `internal/lettering/fonts/` (OFL), and the
  `github.com/tdewolff/canvas` dependency (pure-Go vector drawing with WOFF2 fonts, vertical Japanese via
  `RichText.SetWritingMode(canvas.VerticalRL)`, PNG and PDF renderers — spike verified).
- **Gate A complete in the current Phase 1 worktree:** durable spend reservations and reconciliation (`0200`),
  strict spend-limit validation, stored-token re-resolution, Studio data services, fixed OpenRouter text,
  deterministic placeholder generation, provider lifecycle UI, project/adaptation wizard, job status/retry,
  editable persisted manuscript and real dashboard project data are implemented.
- **Root prerequisites complete:** the stored-token resolver now rechecks revocation, expiry and active ownership
  without the raw secret and preserves worker-token semantics. ADR 0005 freezes the Gate A OpenRouter model,
  endpoint, structured-output request, context/output bounds, price ceilings, hard reservation estimate and
  non-generation key test.
- **Gate A integrity and authorization hardening complete:** generated jobs carry the enqueue-time chapter revision,
  compare it under the persistence row lock and cannot overwrite a newer manuscript. Studio/dashboard routes and
  manual retry are session-only; bearer job reads and cancellation enforce scopes and project allow-lists.
- **Gate A verification complete:** the full Go suite and focused race tests pass with both SQLite and PostgreSQL;
  Go build/vet, module and templ drift, edition checks, frontend check/test/build, strict UI audit, and worker tests
  are green. The placeholder browser flow proves create → generate → edit → reload.
- **Contract addition now present in `internal/studio/contract.go`:**
  `NewProject{Title, Premise, Logline, Style, Format, PageSize, Language string; Genres, Tags []string; PageWidth,
  PageHeight int; SourceText string}`, `NewCharacter{ProjectID, Name, Kanji, Role, RoleLine, Age, Look, Personality,
  SpeechStyle, Category string}` and the Service methods `CreateProject(ctx, actor, NewProject) (Project, error)`
  (owner membership in the same tx), `CreateChapter(ctx, actor, projectID, title) (Chapter, error)`,
  `CreateCharacter(ctx, actor, NewCharacter) (Character, error)` and `SourceText(ctx, projectID) (string, error)`.
  The Gate A end-to-end browser test exercises them; the full Phase 1 demo seed remains Gate B/C integration work.
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

`gen.ScriptRequest.ExpectedUpdatedAt` (optional) binds an enqueue to the chapter revision the caller validated;
`gen.EnqueueScript` rejects a changed chapter and a second queued/running script job for the chapter inside the
enqueue transaction (`jobs.EnqueueParams.Guard`, `gen.ErrScriptJobActive`).

Gate A's story form uses `studio.EditScript` with an optimistic chapter revision.
It updates only the existing script structure and preserves scene/panel IDs. Job
rediscovery uses `jobs.Filter.ChapterID` together with project, user and type filters;
the UI never scans an arbitrary project-wide prefix for the active chapter job.

Region edits identify the unique target layer, not client-supplied page/panel ancestry. `gen.RegionEditRequest`
contains `ProjectID`, `LayerID`, mask, prompt and generation options. Its `jobs.ScopeRegion` requires `ProjectID`,
`LayerID` and a non-null region object, while forbidding chapter/page/panel IDs. At enqueue and persistence, Studio
resolves `layer → panel → page → chapter → project`, verifies the resolved project, checks every lock and rejects
lettering layers. Server-derived ancestry may be indexed internally; it is never trusted from the request.

### Additive Gate B/C contract clarifications (2026-11-07)

These additions are frozen before parallel implementation; existing Gate A signatures remain compatible.

- `studio.AuthorizeTx(ctx, tx, actor, projectID, action)` and `CheckScopeWritableTx(ctx, tx, scope)` expose the
  existing single authorization/lock boundary inside enqueue and lease-fenced persistence transactions. The latter
  locks the resolved ancestor rows on PostgreSQL. Neither method opens an independent transaction.
- `studio.Character.UpdatedAt` and `gen.CharacterSheetRequest.ExpectedUpdatedAt` bind character work to a revision.
  `studio.UpdateCharacterInput` carries project/character IDs, `Character NewCharacter`, and `ExpectedUpdatedAt`;
  `BibleService.UpdateCharacter(ctx, actor, input)` returns the stored character. `GetCharacterTx(ctx, tx, id)`
  locks/loads it. `BibleService` is an additive registry interface for reference/profile/outfit/setting mutations.
- `studio.NewCharacterRef` carries project/character/asset IDs, kind, label, portrait selection and expected character
  revision. `AddCharacterRef(ctx, actor, input)` authorizes and validates an image from the same project, then bumps
  the character revision. `NewOutfit` carries project/character/name/description/asset, default selection and expected
  revision; `CreateOutfit` has the same authorization/revision boundary. `NewSettingItem` carries project/kind/name/
  description/asset/tags; `CreateSettingItem` authorizes and validates the same-project asset.
  Profile/outfit/setting text follows `ActEditScript`; image reference mutations follow `ActEditArt`.
  `BibleService.CreateOutfitTx` and `CreateSettingItemTx` accept the same inputs in a caller transaction, allowing
  staged upload-row insertion and its attachment to commit atomically.
- `ai.ImagePlan.RoutingMode` preserves the effective request policy independently of explanatory JSON, like text.
  It also carries `Needs`, `PublicSeries`, `PinProviderID`, and `PinModelID`; `ImagePlanRequest` accepts optional
  `CharacterIDs` and `PanelType` for scoped routing pins. Durable generation payloads preserve these typed fields
  and every retry/fallback revalidates them. The explanatory `Decision` never authorizes a route.
  Local only rejects remote HTTP/custom text calls and worker dispatch/claims; until same-host worker locality can
  be proven, the deterministic in-process placeholder is its safe image route. Balanced/best/fastest/free-only may
  use an explicitly connected worker, with no paid remote fallback from a zero-spend action.
  New image jobs have a bounded retry/fallback envelope included in their aggregate estimate/approval; old text
  jobs retain their original three-attempt envelope. AI provides `jobs.ManifestLookup` for app worker-claim wiring.
- Assets gain `Stage(ctx, SaveParams) (*StagedAsset, error)`, `StagedAsset.Asset() Asset`,
  `CommitStagedTx(ctx, tx, staged) (Asset, error)` and `DiscardStaged(ctx, staged) error`. Storage I/O happens before
  the job's commit fence; only the asset-row INSERT occurs inside it. Failed fenced writes discard staged objects.
- `lettering.BubbleLayout.TextPath` is the authoritative outline of shaped base/ruby glyphs in page pixels, shared
  by editor SVG and export. Layout also carries rotation/center and text stroke/width; `TextLine.FontSize` records
  ruby size for accessible text. Browser font metrics never independently reflow an exported bubble.
- Page document coordinates remain pixels. The existing DB percentage geometry columns are encoded/decoded at
  the store boundary; `panels.border`/`bubbles.style` retain their document metadata, bubble `style.shape` retains
  shape, and `bubble_texts.fit.ruby` retains ruby. Direction is derived from the project format. Snapshots contain
  the full URL-free `pagedoc.Page`. No migration is needed for these existing JSON extension points.
- KDP trims use exactly 0.125 in = **3.175 mm** bleed. PDF output includes actual trim/bleed boxes, with MediaBox
  enclosing the bleed. A maintained PDF boxing dependency may be added by root if the canvas writer lacks this API.
- `pagestore.ReplaceChapterPagesTx` atomically checks all affected locks and replaces one chapter's pages, avoiding
  nested transactions when pages.from_script runs under the job fence. `export.QueueService.Enqueue(ctx, actor,
  Request)` queues read-only `ScopeProject` exports so locked chapters remain exportable; persistence reauthorizes
  and verifies the captured page revisions. Selected pages retain chapter order. Page art is aspect-fitted within
  each trim without distortion; archives/output remain bounded by the asset service's 64 MiB limit.
- Gate C requests gain optional caller fences: `PagesFromScriptRequest.ExpectedUpdatedAt` and
  `PanelRequest.ExpectedVersion` / `RegionEditRequest.ExpectedVersion`. Enqueue always captures the current
  authoritative chapter/page revision, and checks a supplied fence before creating the job. Durable image job
  input uses a versioned wrapper with a nested typed `request`, actor token ID, immutable scope/revision and image
  plan policy. Candidate sheet previews read `request.character_id`, never infer it from project scope alone.
- `gen.Candidate.Layers []CandidateLayer` carries each independently generated background/character/FX component
  of a whole-panel variation. Each `CandidateLayer` contains layer/kind/character/asset IDs, name, seed and prompt.
  The candidate's main `AssetID` is a composed preview without lettering. Applying a panel variation attaches all
  its component assets atomically at the captured page revision; a layer/region candidate has one component.
  Character sheet candidates have no page layers. Candidate reads/applies reauthorize the live actor/project;
  applying never unlocks, deletes, or alters lettering. Region output restores every pixel outside its mask.

## Design rule (all gates)

Every screen built in Gate B, Gate C and later phases follows the approved design in `../MangaVeyra_All_Pages/`
exactly, as required by "Design fidelity" in `AGENTS.md`. Agents implement the desktop artboards listed in
`docs/SCREENS.md` and do not create new designs; mobile artboards are not final, so phones get the responsive
version of the desktop design. On the owner's decision (2026-11-07) there are no milestone
simplifications: the wizard (B3), story editor (C1) and Models page (G2) follow their artboards, and parts that
depend on unbuilt features use the disabled "Soon" state (ADR 0004 §7). See `../MangaVeyra-design-alignment.md`.

## Waves and ownership

Each agent works in its own git worktree and branch, edits only its paths, and adds new per-module locale files
(`internal/i18n/locales/{en,ja}/<module>.json`) and, if needed, one new stylesheet `web/src/styles/<module>.css`
plus one `@import` line in `web/src/styles/index.css`. Root owns `internal/core/**`, `internal/app/**`,
`internal/assets`, `internal/pagedoc`, all contract files, `go.mod/go.sum` (agents may add a dependency and report
it), every migration, setup integration and final integration.

### Root prerequisites

Before feature work, root lands and verifies the spend-reservation store and `0200` migration, settings validation,
stored-token resolver, canonical contracts and plan alignment. Root also owns setup integration, app/core wiring and
all migrations; feature agents do not edit those paths.

### Gate A · Wave A1 (parallel foundations)

- **AI text** — `internal/ai/**` except `contract.go`: provider registry and sealed OpenRouter keys, deterministic
  placeholder text, ADR-0005 OpenRouter text, routing/local-only guards, spend integration and minimal
  provider-connect flow. `TextPlan.RoutingMode` is retained and enforced at dispatch; `Decision` is never an
  authorization input.
- **Studio script** — `internal/studio/**` except `contract.go`: project/owner membership, chapter, minimal character
  inputs, private adaptation source text, script persistence, authorization and `CheckScopeWritable`. Complete and
  verified in the current worktree on SQLite and PostgreSQL.

### Gate A · Wave A2

- **Complete · Generation** — the `script.generate` path in `internal/gen/**`, using lease-fenced, revision-bound
  atomic persistence.
- **Complete · Minimal UI and acceptance** — provider connection, project creation and story editor prove the
  placeholder/OpenRouter → editable persisted script path. All plan-0001 acceptance criteria are green.

### Gate B (after Gate A is green)

- **AI image expansion** — `internal/ai/**` except `contract.go`: model catalogue, placeholder/OpenRouter/fal image,
  OpenAI-compatible text such as Ollama, local-worker image through child jobs, full routing modes, licence guard,
  explanations and fallback.
- **Studio expansion** — `internal/studio/**` except `contract.go`: character refs/outfits/uploads, setting items and
  the remaining B1/B2/B3/C1–C5 screens not needed by Gate A.
- **Page pipeline** — `internal/pagestore/**`, `internal/lettering/**`, `internal/compose/**`, `internal/export/**`:
  page persistence with locks and snapshots, lettering engine with embedded fonts, compositor, PNG/PDF/CBZ export
  with trims and credits page, the `export.chapter` job and the E2 export screen route.

The `0200` migration, spend store and `setup.ModelStep` are root-owned prerequisites/integration, not feature-agent
ownership.

### Gate C (parallel, after Gate B merges)

- **Generation** — `internal/gen/**`: character sheet, pages-from-script (layout suggestion, prompts from the
  script, auto-lettering), panel art with variations, region edit; candidates and apply.
- **Editor** — `internal/editor/**`, `web/src/islands/editor/**`: page JSON API over pagestore + lettering layouts,
  the D1 editor island (PixiJS canvas, filmstrip, layers, properties, change scope), D2 layout picker, D8
  variations, D13 region edit (mask painting), D3 lettering mode, undo/redo, shortcuts.

### Final integration (root)

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
