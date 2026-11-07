# First playable browser verification

Build the app with `make build`, then run `npm ci` and `npm test` in this directory. Install Playwright Chromium once
with `npx playwright install chromium`, or use an existing Chrome with `MV_BROWSER_CHANNEL=chrome npm test`.

The suite starts its own Go server on port 6199 and a newly allocated temporary SQLite database. It clears the
configuration path, master key and SMTP host, supplies a test-only worker token, and refuses to reuse a running
server. It never reads the operator's existing application data or provider keys. The temporary data directory is
removed after the run. `MV_E2E_BASE_URL` can select another unused loopback port.
`MV_E2E_BINARY` can select an already-built app executable without replacing another preview's binary.

The flow completes setup without keys, all five wizard steps, a placeholder script, four character sheets and
selection, pages from the saved manuscript, an independent character layer, four panel variations and selection,
painted region edits, structured bubbles with undo/redo, conflict recovery, page and lettering-layer locks, and
PDF/CBZ/PNG-ZIP downloads with the correct filenames. The download checks inspect actual PDF trim/bleed dictionaries
and archive central directories. The browser blocks unexpected external HTTP requests, checks the lazy editor load
and captures runtime errors. The native Chromium
optional-view-transition abort is excluded by its exact browser error message.

All four custom-provider steps run against a temporary loopback HTTP fixture that only returns an original test
model catalogue. The administrator enables private provider URLs through the designed switch; no real provider is
contacted and the fixture asserts that the sole call is a non-generation model-list request. A separate browser
context checks server-rendered screens and the editor's read-only proof with JavaScript disabled.

The Phase 2 activity step inspects real generation/export jobs in G9, the action menu's keyboard recovery, bounded
job details and events, G4 category/empty states, explicit read actions and the unread counter. It captures both
screens in English/Japanese and checks basic narrow-width usability. Both routes also join the no-JavaScript pass.

The content-language flow creates a manuscript in each of the ten supported languages in both English and Japanese
interfaces. It verifies localized dialogue, the shared shaped outlines, automatic vertical lettering, a no-JavaScript
page proof and Unicode download names. Every language exports PDF, CBZ and PNG ZIP; PDF is repeated in the Japanese
interface. The matrix uses Local only mode and the built-in placeholder, including when the preceding flow has
connected its fake provider.

Artifacts and traces live in ignored `test-results/` and `playwright-report/`. Screenshots use 1440×900 desktop.
Phone layouts and touch-target testing are deferred to the final optimisation pass, following the owner's
2026-11-07 instruction and plan 0004; the existing phone step stays skipped.

## Desktop reference evidence

The approved HTML handoff deliberately stays outside the repository. To capture its artboards using local fonts,
icons and artwork, start an isolated app server and run from the repository root:

```sh
MV_BROWSER_CHANNEL=chrome MV_E2E_BASE_URL=http://127.0.0.1:6199 \
  node e2e/scripts/capture-artboards.mjs
```

`MV_DESIGN_DIR` overrides `../MangaVeyra_All_Pages`; `MV_DESIGN_SCREENSHOTS` overrides the ignored output directory.
Only the reference-rendering context bypasses CSP, because the handoff is authored with inline styles. Application
screenshots and the test flow retain production CSP. External requests are blocked in the reference renderer too.
Compare the resulting desktop references alongside the matching app captures. Design notes are maintained in
`docs/plans/0004-phase-1-qa-fixes-and-content-languages.md` and ADR 0004 §8.
