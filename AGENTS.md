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
