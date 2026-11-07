# Phase 1 QA fixes and manga content languages

- Status: Active
- Branch: `phase-1/first-playable` (PR #7); starting commit `addcd9e`.
- Normative inputs: AGENTS.md, plan 0002/0003, ADRs 0001–0007, the approved desktop handoff in
  `../MangaVeyra_All_Pages/`.

## Outcome

1. Every defect found by the 2026-11-07 PR #7 QA pass is fixed on desktop, and every undocumented deviation from
   the desktop artboards on the Phase 1 screens is removed or, where a runtime constraint forces it, recorded in
   ADR 0004 §8 with its reason.
2. A creator can write, letter and export manga in English, Japanese, Korean, Simplified Chinese, Traditional
   Chinese, Spanish, French, German, Portuguese and Hindi. Every script renders with real glyphs in the editor,
   the no-JS proof, previews and every export format; there is no tofu for any supported language.

Out of scope (owner, 2026-11-07): phone/mobile layouts and touch targets (deferred to the end-of-project
optimisation pass), new interface locales (the UI stays English + Japanese), the D5 translation mode, publishing,
and remote-provider live calls. GitHub Actions stay disabled while the repository is private; local gates replace CI.

## Shared contracts (root-owned)

- `internal/pagedoc/languages.go`: `ContentLanguages` = `en, ja, ko, zh-Hans, zh-Hant, es, fr, de, pt, hi`;
  `IsContentLanguage`, `NormalizeContentLanguage`, `VerticalAllowed` (ja, ko, zh-Hans, zh-Hant) and
  `VerticalByDefault` (ja, zh-Hant). Every Go language list, validation and vertical decision uses these.
- Lettering font keys (strings shared by Go, the editor island and stored bubble styles): existing `comic`
  (Comic Neue Bold), `sfx` (Bangers), `jp` (Noto Sans JP Black) plus `ko` (Noto Sans KR), `sc` (Noto Sans SC),
  `tc` (Noto Sans TC), `deva` (Noto Sans Devanagari). Every key renders any supported script through per-run glyph
  fallback across all bundled fonts. Default key for a new bubble: ja→`jp`, ko→`ko`, zh-Hans→`sc`, zh-Hant→`tc`,
  hi→`deva`; Latin languages → `comic`, or `sfx` for SFX bubbles.
- Language display names use each module's own locale keys (`<module>.language.<tag>`) in both en and ja.
- Desktop-only: acceptance is the 1440×900 desktop artboard. Phone regressions are not introduced deliberately but
  are not tested or fixed in this plan.

## Milestones and ownership

Agents work in separate git worktrees from the commit that adds this plan, never edit outside their list, make
logical commits, and return changed files, tests run and the ADR 0004 text they need. The root integrator alone
edits `docs/**`, `README.md`, `go.mod`/`go.sum`, `web/package*.json`, `internal/core/**`, `internal/app/**`,
`internal/pagedoc/languages*.go`, `components.css`, migrations, and cherry-picks results.

1. [ ] **L — Languages and lettering.** `internal/lettering/**` (fonts, fallback, shaping, line breaking for
   Korean/Chinese/Devanagari, vertical punctuation), `internal/compose/**` (empty panels render the same as the
   editor), `internal/export/service.go|http.go|contract.go` (Unicode filenames with ASCII fallback, language
   options and ComicInfo for all languages), `internal/studio/**` (project language validation), `internal/gen/**`
   and the placeholder text provider file in `internal/ai/` (placeholder scripts in the project language with
   dialogue, so auto-lettering runs without keys), `internal/seed/**` (demo label overlap), `internal/pagedoc/**`
   except languages.go. Font binaries may be added under `internal/lettering/fonts/` with OFL notices.
2. [ ] **E — Editor (D1/D2/D3/D8/D13 + no-JS proof).** `web/src/islands/editor/**`, `web/src/styles/editor.css`,
   `internal/editor/**`, editor locales. Top-bar overflow, bubble placement, localized layer names, font/vertical
   defaults per contract, panel/region generation de-duplication, every D-screen gap from the QA report.
3. [ ] **S — Studio and models (B2/B3/C1–C5/G2/G11/G12).** `internal/studioui/**`, `internal/ai/**` UI and
   routing files (not the placeholder text file), `internal/web/ui/**`, `web/src/lib/**` (not export.ts),
   studio/models styles and locales. Story→editor dead end, C1 dialogue lines, wizard language options, live
   counters, G11 placeholder decisions, G12 flow and private-endpoint path, every B/C/G gap from the QA report.
4. [ ] **X — Export screen (E2).** `internal/export/templates.templ` (+ generated), `web/src/styles/exportui.css`,
   `web/src/lib/export.ts`, exportui locales. The editor shell behind the modal matches D1; modal gaps; language
   labels for all ten languages.
5. [ ] **Root integration.** Cherry-pick, resolve, ADR 0004 §8 and ADR 0008 (content languages and fonts), README,
   full gates and a fresh browser QA pass at 1440×900 in English and Japanese UI with a project in every language.

## Acceptance and verification

- Each listed QA defect has a regression test where it is testable (Go, vitest or the Playwright suite).
- `gofmt -l cmd internal` empty, `go vet ./...`, `go test ./...`, `templ generate` with no drift,
  `go test -count=1 ./internal/editioncheck/...`, `npm run check && npm test && npm run build` in `web/`, worker
  pytest, and the e2e suite.
- Lettering golden tests prove real glyphs (no `.notdef`) for a sample sentence in every content language,
  correct Devanagari conjunct shaping, Korean/Chinese line breaking, vertical forms for ja/zh-Hant punctuation.
- Desktop screenshots of every Phase 1 screen compared with its artboard; remaining differences are documented.

## Risks

- Bundled CJK fonts grow the binary (record the size in ADR 0008); fonts are OFL and loaded only from the binary.
- Changing language validation must accept every existing stored project (`en`, `ja`) unchanged.
- Generation de-duplication must not merge requests from different users, scopes or prompts.
- Rollback boundary is each logical commit; nothing is merged or pushed without the owner's request.
