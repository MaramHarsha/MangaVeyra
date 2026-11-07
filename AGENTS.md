# MangaVeyra agent instructions

Read `docs/ARCHITECTURE.md` before changing code. For UI work, also read
`docs/SCREENS.md`. For roadmap scope, read `docs/FEATURES.md` and the active plan
under `docs/plans/`. The foundation is implemented, but many feature packages are
still intentional stubs; never describe a roadmap feature as shipped until its
runtime path and tests exist.

## Product invariants

- This repository never contains payment, price, credit, wallet, subscription,
  tip, payout, checkout, or upgrade UI. World may be labelled `Free`,
  `Paid · on World`, or `Owned`, without showing a price.
- A page contains polygon panels; a panel contains independent background,
  character, FX, and lettering layers. Lettering remains structured text.
- Every AI action has one explicit scope. Server-side page, panel, and layer locks
  must be enforced before work is queued or persisted.
- Preserve honest human/AI provenance.
- `local_only` must prevent prompts, images, and metadata from leaving the server.
- No telemetry or unconfigured outbound network calls.
- Publishing, deleting, sharing, and unlocking always require a human action.
- Server-rendered screens must be complete without JavaScript except documented
  rich islands.
- Preserve WCAG AA contrast, visible focus, reduced-motion behavior, and mobile
  touch targets.
- Samples, seeds, and tests use only original MangaVeyra characters and titles.

## Design fidelity (required for every UI change)

The product already has an approved, high-fidelity design. Build to it; do not
create a new design.

- The design is the HTML handoff in `../MangaVeyra_All_Pages/` (on the owner's
  machine: `/Users/harsha/Desktop/Project-Ideas/MangaVeyra_All_Pages/`). It stays
  outside this repository on purpose: it also contains World platform screens with
  payment UI that must never enter this repo. Each screen is a
  `div[data-screen-label="ID – Name – Platform"]` in a `.dc.html` file;
  `docs/SCREENS.md` maps every screen ID to its file and route. Tokens and rules:
  the bundle's `README.md` and `04 Design System.dc.html`.
- Build to the **desktop** artboards. They are the source of truth. The mobile
  artboards are not final (owner, 2026-11-07): do not build to them yet; phones
  get the responsive version of the desktop design using the existing responsive
  patterns.
- Before building or changing a screen, open its desktop artboard and match it:
  layout and grid, component choice, hierarchy, copy, iconography, states, and
  navigation. Do not invent new layouts, colours, fonts, icon sets,
  illustrations, spacing scales, or components where the design defines them.
- Use only the existing tokens (`web/src/styles/tokens.css`) and UI kit
  (`internal/web/ui`). A token or component the design lacks needs a written
  reason in the plan and must follow the design system's patterns.
- The owner's decision (2026-11-07): the design is final and deviations are not
  allowed. Do not simplify a designed screen for a milestone (no one-page
  versions of multi-step flows, no form instead of a designed editor). If part of
  a designed screen depends on a feature that does not exist yet, build the full
  designed screen and show that part in the disabled "Soon" state.
- Use the design's own copy. Where the design has no artboard for a necessary
  state (an error, an empty or offline case), build it only from the design's
  components and patterns, record it in
  `docs/decisions/0004-design-addendum.md`, and keep it visually consistent with
  the nearest designed screen.
- Navigation keeps the design's items and order. A destination that is not built
  yet stays visible as a disabled "Soon" item; never ship a link or button that
  leads to a 404 or a missing endpoint.
- Before calling UI work done, compare it side by side with the desktop artboard
  at 1440×900, check that it stays usable at 390×844, and record remaining
  differences in the PR description.
  `../MangaVeyra-design-alignment.md` lists the current gaps.
- If the design folder is not readable from your environment, stop and ask the
  user for access. Do not guess or redesign.

## Architecture and file boundaries

- Feature packages do not import `internal/app`. Share narrow services through
  `core.Provide` and `core.Use`.
- Write portable SQL with `?` placeholders and keep SQLite/PostgreSQL behavior
  aligned.
- Never expose or log decrypted provider keys, tokens, or secret-bearing prompts.
- UI text belongs in both English and Japanese catalogues.
- Commit generated `*_templ.go` beside its `.templ` source.
- Shared hotspots are owned by the root/integrator unless a task explicitly says
  otherwise: `internal/core/**`, `internal/app/**`, migrations, `go.mod`, `go.sum`,
  `web/package-lock.json`, shared locale files, CI, and repository-wide generated
  output.
- Do not modify remotes, push, merge, open a PR, or rewrite Git history unless the
  user explicitly asks.

## Multi-agent workflow

- The root agent owns the brief, architecture decisions, shared contracts,
  integration, and final verification.
- Delegate only bounded, independent work. Every editing agent gets an exact file
  ownership list, acceptance criteria, and test command.
- Never let two agents edit the same file or worktree concurrently. Prefer one
  editing agent plus read-only explorers/reviewers unless separate Git worktrees
  have been assigned.
- Review agents are read-only unless explicitly given a follow-up fix.
- An agent returns changed files, tests run, failures or skipped checks, and
  unresolved risks. Agreement is not evidence; tests and inspected behavior are.
- For multi-step milestones, follow `PLANS.md` and keep the active contract under
  `docs/plans/`.

## Verification

Run the smallest relevant checks while working. Before integration, run every
applicable gate:

- `gofmt` check, focused Go tests, `go vet ./...`, then `go test ./...`.
- SQLite and PostgreSQL coverage for data-layer changes.
- `npm run check`, `npm test`, and `npm run build` in `web/` for frontend changes.
- `python3 -m pytest -q` in `worker/` for worker changes.
- `templ generate`, then verify generated files have no drift.
- `go mod tidy`, then verify `go.mod` and `go.sum` have no unintended changes.
- `go test -count=1 ./internal/editioncheck/...` for any UI or locale change.
- A focused end-to-end flow before calling a milestone complete.

If a check cannot run because a local tool is missing, report that separately
from code failures. Do not silently install dependencies or contact external
providers without permission.
