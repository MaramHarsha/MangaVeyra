# First playable browser verification

Build the app with `make build`, then run `npm ci` and `npm test` in this directory. Install Playwright Chromium once
with `npx playwright install chromium`, or use an existing Chrome with `MV_BROWSER_CHANNEL=chrome npm test`.

The suite starts its own Go server on port 6199 and a newly allocated temporary SQLite database. It clears the
configuration path, master key and SMTP host, supplies a test-only worker token, and refuses to reuse a running
server. It never reads the operator's existing application data or provider keys. The temporary data directory is
removed after the run. `MV_E2E_BASE_URL` can select another unused loopback port.
`MV_E2E_BINARY` can select an already-built app executable without replacing another preview's binary.
The server runs in production mode (no `--dev`) so the service worker and offline reading behave as they do for
real readers.

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

The Phase 3 publish-and-read flow (`publish-read.spec.ts`) switches the server to public mode, seeds the demo with
`mangaveyra seed --demo` and creates a reader with `mangaveyra user create` against the suite's own data directory.
The creator publishes chapter 1 from E1 (checking the drafted alt-text), the reader follows from E5, chapter 2 notifies
the follower, the reader reads chapter 1 in guided mode with alt-text, downloads it and reads it again with the network
off from the service worker cache. An anonymous visitor reads the public chapter; an edit made through the editor API
after publishing never reaches readers; members-only and private series return 404 to visitors (and private to a
non-member reader); a mature series is hidden from visitors and asks the member to confirm their age once.

The Phase 4 flow (`run-a-server.spec.ts`) seeds the demo, has the admin create an invite in G5 and a new member sign up
through it, adds the member as a letterer limited to the lettering layer, posts a panel review comment that the owner
marks Needs changes (the member's art edit is refused), creates a project-scoped agent token in G13 and calls `/mcp`
over JSON-RPC to edit a script panel, checks the saved value and the human Undo action, then verifies that a publish
request waits in G13, backs the server up and restores that archive from G10, and checks that a build without
a release key never checks for updates. Signed installs and rollbacks run against a fake release in
`internal/update`'s integration tests.

The release smoke flow (`release-smoke.spec.ts`) draws two original test PNGs locally and imports them through G10.
It opens the resulting editor and verifies page dimensions, retained art, human provenance and anonymous access
refusal. It also checks 27 implemented routes with JavaScript disabled and captures the imported page's server-rendered
proof. Basic document-overflow checks at 390×844 cover Projects, Models, Notifications and Backup/import; the complete
phone touch-target pass remains deferred as described above. Templates stays a disabled Soon item and is not an
implemented route. Run these focused operations checks with `npm test -- tests/release-smoke.spec.ts tests/run-a-server.spec.ts`.

