# MangaVeyra — instructions for Claude

Follow [AGENTS.md](AGENTS.md): it holds the product invariants, file-ownership rules and verification steps for every
coding agent. Before changing code read `docs/ARCHITECTURE.md`; for UI also `docs/SCREENS.md` and
`docs/decisions/0004-design-addendum.md`; for scope `docs/FEATURES.md` and the active plan in `docs/plans/`.

Verify with `make test` (Go on SQLite, web vitest, worker pytest). Commit generated `*_templ.go` files next to their
`.templ` sources.
