# Phase 1 desktop QA and content-language verification

Date: 2026-11-07. Contract: [plan 0004](../plans/0004-phase-1-qa-fixes-and-content-languages.md).
Continued Claude's saved work from integrated head `7c7ac86`, including the language agent's unintegrated
placeholder commit `de927a1` and its saved lettering changes. The plan's scope, language contract and desktop
acceptance criteria are unchanged.

## Completed fixes

- Bubble auto-fit follows the safe interior of ellipse, whisper, cloud, burst, caption and SFX shapes. Positioned
  glyph bounds, ruby and text strokes decide whether a size fits; vertical columns share their top edge. A recovered
  Korean vertical SFX failure was caused by the text stroke not being reserved during wrapping and is fixed.
- The independent outline test checks 672 combinations of content language, shape, box, orientation and font size,
  including the reported 14-point setting. All supported glyphs exist, and fitted glyphs remain inside the outline.
- Placeholder scenes have deterministic original opening beats in every content language and actual camera/shot
  values understood by the editor. A named character appears in its panel's character list.
- Converting an auto-placed caption to Speech or Shout gives its previously unused tail a nearby tip. Existing
  positioned tails are preserved. This is covered by frontend tests and the saved-document browser flow.
- Story reads keep the manuscript and chapter revision in sync when generation completes during loading. Recovery
  is bounded and rechecks membership; genuine concurrent edits still return a conflict. Deterministic tests cover
  completion before and after the manuscript read, revocation, locks and continuously changing revisions.
- The permanent browser suite includes the ten-language matrix in both interface languages, no-JavaScript proofs,
  shaped outlines, vertical defaults, Unicode filenames and actual PDF/CBZ/PNG ZIP downloads.

## Verification

| Gate | Result |
|---|---|
| `gofmt -l cmd internal`, `git diff --check` | Clean |
| `go vet ./...`, `go test -count=1 ./...` | Pass; 36 tested packages |
| Full Go suite with `MV_TEST_POSTGRES_URL` | Pass on SQLite and isolated PostgreSQL 18; 36 tested packages |
| `templ generate` | No generated-file drift |
| `go mod tidy` | No `go.mod` or `go.sum` changes |
| `go test -count=1 ./internal/editioncheck/...` | Pass |
| Web check, unit tests, production build | Pass; 103 frontend tests |
| Worker pytest in the existing installed environment | 153 pass; 2 optional-backend skips |
| Browser language matrix alone | Pass; 20 manuscripts, 20 PDFs, 10 CBZs and 10 PNG ZIPs |
| Final combined browser suite | Pass; 2 flows in 3.1 minutes, including all 20 language cases and 40 matrix downloads |

The worker skips are the optional torch and WebSocket backends; no dependencies were installed. The initial
sandboxed worker attempt could not bind localhost sockets; the same suite passed with local socket access. Test
servers and databases are isolated from the owner's app data. The PostgreSQL container was removed afterward.

## Desktop comparison

Captured the approved external handoff with `e2e/scripts/capture-artboards.mjs`, using local fonts and icons.
Compared the matching runtime captures at 1440×900. Runtime data, no-key states and the existing disabled Soon
controls account for the differences below; their detailed behavior is recorded in [ADR 0004 §8](../decisions/0004-design-addendum.md).

| Screen | Comparison and existing runtime differences |
|---|---|
| B2 Projects | Designed grid, filters and metadata; real project/member counts and statuses. |
| B3 Wizard | Designed steps, format cards and summary; one planned chapter, no saved draft, real routing state. |
| C1 Story | Chapter/scene tree and compact manuscript; actual panel count, scene details and Soon assistant/arcs/import. |
| C2 Character bible | Designed cards and role/name hierarchy; kept reference artwork, Soon training/lock score. |
| C3 Character detail | Designed portrait/profile/outfit hierarchy; actual references and Soon relationship/training data. |
| C4 Create character | Designed form and four-reference picker; working keep action, real route and Soon training. |
| C5 Setting | Designed tabs and library components; a fresh project uses the documented empty state. |
| G2 Models | Designed provider tiles and model table; actual catalogue, connection state and route estimates. |
| G11 Routing | Designed decisions/pins/guards columns; real decisions, private-endpoint control and editable guards. |
| G12 Custom provider | Designed four-step shell and test result; OpenAI-compatible fields, documented private-network switch. |
| D1 Editor | Designed three-column workspace, top bar, layers and footer; actual document, host, worker and scope state. |
| D2 Layout picker | Designed modal/tiles; suggestions reflect the page's beat count, custom/saved layouts stay Soon. |
| D3 Lettering | Designed inspector and reading order; seven fonts, content-language vertical switch and documented geometry details. |
| D8 Variations | Designed four-up tray and queue; actual timings/model, no invented Character Lock scores. |
| D13 Region edit | Designed compare handle and variation strip; actual layer aspect ratio and documented brush/coordinate details. |
| E2 Export | Designed modal over the editor shell; documented CBZ/trim/DPI options, real counts and Soon formats. |

The language matrix captures C1, D1, D3, E2 and the no-JavaScript page proof in both interface languages for every
content language. Lettering font/shape proof sheets are generated by Go tests with `MV_RENDER_QA_DIR`.

Local evidence from this run is under `/private/tmp/mv-claude-resume-evidence/`: `artboards/`, `comparison/`,
`lettering/` and `browser/`. Standard future runs write browser artifacts to ignored `e2e/test-results/`.

## Remaining separate work

After this desktop run, the owner separately authorized live OpenAI verification. Three direct OpenAI layer jobs
passed with the saved key, real manga artwork, character/FX transparency and reconciled spend; see
[the live QA report](openai-live-2026-11-07.md). The remaining token/reference/mask/multi-output/backup checks in
[ADR 0009](../decisions/0009-openai-image-route.md) are still pending.
Mobile optimisation remains deferred, and GitHub Actions remain disabled while the repository is private.
No push or PR update is part of this continuation.
