# Phase 4: Run a server for others

- Status: Active
- Branch: `phase-4/run-a-server`; base `eb6a1fc` (the Phase 3 branch, not yet merged into `main`).
- Normative inputs: AGENTS.md, ADRs 0001–0013, the approved desktop handoff in `../MangaVeyra_All_Pages/`.

## Outcome

An admin runs a private group server with invites, moderation, backups and one-click updates; members collaborate
and review pages together; agents work in projects within limits; existing comics can be imported.

Out of scope: the mobile artboards (deferred mobile pass), translation, EPUB/video (Phase 5), the MCP client and
plugin directory (v2), World.

## Screens (desktop artboards)

| Screen | Route | Owner |
|---|---|---|
| G5 Admin & moderation, Users, Content rules, Server | /admin, /admin/{reports,users,disputes,rules,server} | admin |
| G6 Maintenance | maintenance gate | admin (gate exists in core) |
| G10 Backup & import | /admin/backup | backup |
| G14 Update available / Updating / Done / Failed, member banner | admin login modal, /admin/update | update |
| G13 Agents & MCP | /agents | mcp |
| D7 Collaboration & review | editor ?panel=review | collab (editor) |
| G2/G12 provider entries | /models | providers (ai) |

## Shared contracts (root-owned, frozen)

- `internal/backup/contract.go`: `backup.Service` (Create, Get, List, Restore, Delete), kinds, statuses, errors.
- ADR 0013 decides admin scope, content rules, maintenance, backup format, import limits, signed updates, MCP
  approvals, collaboration and provider rules.
- New migrations are root-owned (0600 admin/update/backup, 0700 mcp, 0900 import, 1000 collab) and need a failing
  test that proves them necessary; agents send the SQL.

## Milestones and ownership

Agents work in separate worktrees from the commit that adds this plan and edit only their files. Root alone edits
`docs/**`, `README.md`, migrations, `internal/core/**`, `internal/app/**`, `internal/web/layout/**`,
`go.mod`/`go.sum` (agents may run `go get` for the one dependency named in their brief and report the lines),
`web/package*.json` and `e2e/**`.

1. [x] **A — Admin.** `internal/admin/**`: G5 tabs, invites and sign-up, report resolution and takedowns, AI-label
   disputes, content rules (with the narrow `publish` enforcement they need), server status, maintenance settings.
2. [ ] **B — Backup and import.** `internal/backup/**`, `internal/importer/**`: `backup.Service`, archive format,
   schedules, restore, export everything, per-user export, `mangaveyra backup`, G10; CBZ/CBR/PDF/image import.
3. [ ] **U — Update.** `internal/update/**`: signed manifests, opt-in checks, G14 states, member banner, binary
   self-update with backup/migrate/health/rollback, container guidance, opt-in patch auto-update.
4. [x] **M — Agents and MCP.** `internal/mcp/**`: G13, agent tokens UI, `/mcp`, tools, approvals, agent labels and
   undo, `mcp-stdio`.
5. [x] **C — Collaboration.** Editor review drawer (D7), internal panel comments and statuses, live cursors, scoped
   roles.
6. [x] **P — Providers.** Google, xAI, Replicate, Together, Runware, Hugging Face, generic HTTP, ComfyUI adapters with
   fake-transport tests.
7. [ ] **Root integration.** Navigation, contracts, migrations, e2e (invite → collaborate → back up → update with
   rollback), ADR 0004 records, README, full gates on SQLite and PostgreSQL, design comparison, independent review.

## Acceptance and verification

- Phase done when: an admin invites a member, the member and the owner review a page together, the admin backs the
  server up and restores it, and an update from a signed test release succeeds and a broken one rolls back; an agent
  edits a script within its scope and a publish request waits for a person.
- Gates: `gofmt`, `go vet`, `go test ./...` on SQLite and PostgreSQL 18, `templ generate` and `go mod tidy` without
  drift, editioncheck, web check/test/build, worker pytest, the e2e suite.
- Desktop artboard comparison at 1440×900 for every Phase 4 screen, with remaining differences in ADR 0004.

## Risks

- Restore and update act on the whole server: they must be exclusive, run in maintenance mode and always leave a
  working server (old data and binary) when they fail.
- Archives and imports are attacker-shaped input: zip bombs, traversal and decode limits.
- Agents must never gain a human-only action, a project outside their token, or spend above their threshold.
