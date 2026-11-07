# First playable browser verification

Build the app with `make build`, then run `npm ci` and `npm test` in this directory. Install Playwright Chromium once
with `npx playwright install chromium`, or use an existing Chrome with `MV_BROWSER_CHANNEL=chrome npm test`.

The suite starts its own Go server on port 6199 and a newly allocated temporary SQLite database. It clears the
configuration path, master key and SMTP host, supplies a test-only worker token, and refuses to reuse a running
server. It never reads the operator's existing application data or provider keys. The temporary data directory is
removed after the run. `MV_E2E_BASE_URL` can select another unused loopback port.

The flow completes setup without keys, all five wizard steps, a placeholder script, four character sheets and
selection, pages from the saved manuscript, an independent character layer, four panel variations and selection,
painted region edits, structured bubbles with undo/redo, conflict recovery, page and lettering-layer locks, and
PDF/CBZ/PNG-ZIP downloads with the correct filenames. The download checks inspect actual PDF trim/bleed dictionaries
and archive central directories. The browser blocks unexpected external HTTP requests, checks the lazy editor load
and captures runtime errors. The native Chromium
optional-view-transition abort is excluded by its exact browser error message.

All four custom-provider steps run against a temporary loopback HTTP fixture that only returns an original test
model catalogue. The isolated server permits private provider URLs for this fixture; no real provider is contacted
and the fixture asserts that the sole call is a non-generation model-list request. A separate browser context checks
server-rendered screens and the editor's read-only proof with JavaScript disabled. All new desktop routes are also
checked for document overflow and 44px enabled-control touch targets at 390×844.

Artifacts and traces live in ignored `test-results/` and `playwright-report/`. Screenshots use 1440×900 desktop and
390×844 responsive viewports; the unfinished mobile artboards are not a target.

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
`docs/plans/0003-phase-1-completion.md` and ADR 0004 §8.
