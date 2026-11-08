# ADR 0013: Running a server for others

- Status: Accepted for Phase 4
- Date: 2026-11-09
- Boundary: `internal/admin`, `internal/backup` (contract in `contract.go`), `internal/importer`, `internal/update`,
  `internal/mcp`, editor collaboration, provider adapters in `internal/ai`

## Context

Phase 4 lets an admin run MangaVeyra for a group: moderate it, back it up, move it, import existing comics, update
it safely, let agents work within limits, and let collaborators review each other's pages. Several of these touch
every module's data or act on the server itself, so the rules are fixed before they are built in parallel.

## Decisions

1. **Admin (G5).** `/admin` is for site admins only (others get 404). Tabs: Reports, Users, AI-label disputes
   (reports with reason `ai_label`), Content rules, Server. Every admin action is written to `audit_log`. Moderation
   acts on published content: a report can be dismissed, or actioned by hiding a comment, taking a series or chapter
   down, or suspending an account. A takedown hides the content from everyone outside its project (`publish.Service`
   returns `ErrNotFound`) and shows the project a notice; it never deletes creator work. Admins still cannot read
   private projects through the admin panel. Invites (create, revoke, role) and sign-up mode are managed in Users.
2. **Content rules.** The instance settings already hold the rules. When `allow_mature`/`allow_adult` is off, those
   ratings cannot be chosen in E1 or series settings and existing series with them are readable only inside their
   project. `blocked_words` refuses public comments, series titles and synopses that contain them (case-insensitive,
   whole words). `require_ai_labels` keeps every role labelled at publish (already enforced). The NSFW filter needs a
   worker classifier; without one the switch is shown in the Soon state, never as protection that does not exist.
3. **Maintenance mode** keeps using the existing gate: admins get in, everyone else sees G6 Maintenance with the
   admin's message. Restore and update put the server in maintenance mode while they run.
4. **Backups (G10).** An archive is a `.mvbackup` zip: `manifest.json` (app version, schema version, dialect, kind,
   created time, per-entry SHA-256), one JSON-lines file per table, and the media objects. The format is
   dialect-independent, so a SQLite server can move to PostgreSQL and back. `secret.key` and `MV_MASTER_KEY` are never
   included: provider keys, World tokens and 2FA secrets restored on a server with a different master key cannot be
   read, and the UI says so (provider keys must be replaced, 2FA reset). Archives are written with mode 0600 under
   `$MV_DATA_DIR/backups` (or the admin's destination directory) and downloaded by admins only. Schedules keep the
   newest N. Restore accepts archives from the same or an older schema (migrations run afterwards), replaces all data
   in one transaction per table group in maintenance mode, and records the restore. A member can export their own data
   (`user_export`: their projects, characters, published works, comments and reading data) from Settings → Data.
5. **Import.** CBZ, CBR, PDF (through `pdftoppm` when installed; otherwise PDF import is Soon) and image folders or
   zips become a new private project owned by the importer, one page per image in archive order. Archives are read
   with limits: entry count, per-entry and total uncompressed size, no path traversal, no symlinks, images sniffed and
   decoded under the existing decode limits. Panel and bubble detection runs on a worker when one offers it; otherwise
   each page becomes one panel. Imported art is labelled `human` (it was made outside MangaVeyra) and OCR text, when a
   worker provides it, is a draft for the creator to review.
6. **Updates (G14).** Releases are described by a manifest (version, channel, changelog, per-platform download URL and
   SHA-256) signed with Ed25519; the public key is compiled into the binary and an update is refused unless the
   signature and checksum verify. Update checks contact only the configured release URL and are **off until the admin
   turns them on** (Server tab), keeping "nothing leaves the server" true by default. Admins see G14 at login while an
   update is available; members see a small banner only. For the single-binary install the flow is: pre-update backup
   → download and verify → run the new binary's migrations → start it on a loopback port and pass its health check →
   swap the executable atomically and restart; any failure before the swap restores the backup and keeps the old
   binary (G14 "failed, restored"). The pre-update backup is deleted after success unless the admin keeps it for 7 days.
   Container installs cannot replace their own image: G14 takes the backup and shows the exact commands. Automatic
   patch updates at 03:00 are opt-in and limited to patch releases.
7. **Agents and MCP (G13).** The MCP server runs at `/mcp` (streamable HTTP, official Go SDK) and through
   `mangaveyra mcp-stdio`. Agents authenticate with agent tokens (`api_tokens` kind `agent`) that carry scopes, a
   project list and an approval threshold. Tools act as the token's owner within its scopes and projects and through
   the same services, locks and spend reservations as the UI. Publish/schedule, deleting pages or projects, sharing or
   changing roles, and unlocking locked panels are never offered as tools; an agent asking for them creates a pending
   request that a person approves or declines in G13. Generation above the threshold waits for approval the same way.
   Every agent change is labelled with the token in version history and the audit log, and can be undone there. Local
   only mode does not block the MCP server (it receives requests), only the future MCP client.
8. **Collaboration (D7).** The editor's review drawer lists internal panel comments (`comments.visibility =
   'internal'`) with a status per panel (open, needs changes, done, approved), replies and "Approve page". Live
   cursors use the existing WebSocket presence on `doc:page:{id}` and carry only a cursor position and the member's
   name and colour. Roles keep the studio role matrix; a member's role can be narrowed to the lettering layer or a page
   range, enforced by the studio service for every edit, agent and batch action.
9. **More providers.** Google, xAI, Replicate, Together, Runware, Hugging Face, a generic HTTP adapter (request
   template plus response path, synchronous or polling) and ComfyUI on a server the admin names are built behind the
   existing router, capability, licence and spend rules. They are tested against fake transports only; no live call is
   made without the owner's approval. Server URLs pass the existing SSRF guard.
10. **Scope.** Desktop artboards are followed; the mobile artboards stay deferred (ADR 0012 §11).

## Consequences

The admin panel, backups and updates make a private group server practical without shell access. The dialect-free
archive costs time on very large servers but makes moves and restores predictable. Opt-in update checks mean a new
server will not hear about updates until the admin turns checks on; the Server tab says so.

## Implementation notes (2026-11-09)

- **Providers (§9).** Built as frozen routes in `internal/ai/routes.go` and `image_routes.go`, with tariffs, endpoints
  and documentation URLs in each adapter and the table in `internal/ai/REMOTE_IMAGE_BOUNDARY.md`. Imagen is replaced by
  Nano Banana 2.1 because Imagen is shut down on the Gemini API. Runware's quote and the Replicate SDXL quote are
  application bounds, so SDXL is pinned to one version. Hugging Face serverless reserves the tariff the router listed at
  the passed test; dedicated endpoints reserve zero per call because they bill per hour (the UI says so). ComfyUI and
  generic HTTP connections follow ADR 0007's custom-provider rules: local only mode blocks them even on a private
  network, because they are other machines and some ComfyUI nodes call external services. Every route is marked
  "Fake-transport tested · not live-verified" until the owner approves a live check.
- **Content rule defaults (§2).** New servers allow mature series (behind the reader's age confirmation, ADR 0012 §1)
  and keep adult series off until an admin allows them. Ratings read Teen 13+, Mature 17+ and Adult 18+ everywhere.
- **Takedowns (§1)** are stored as module keys in `instance_settings` and enforced by `publish.Service`; `Series` and
  `Publication` carry `TakenDown` so project members see a notice. Collaboration approvals are internal comments with
  status `approved` and the approved page version in their anchor.
- **Backups (§4) as built.** `pre_update` archives hold the database only, so they take well under a second on a
  seeded server; scheduled, manual and export archives also hold media. Restore runs in **one** transaction for the
  whole database (stronger than the per-group wording above): with the same schema it deletes and reloads every table;
  with a different schema it drops the tables, migrates to the archive's version, loads, then runs the calling binary's
  newer migrations — so the old binary restoring a pre-update archive ends exactly at the archive's schema and a later
  update can migrate forward again. A restore keeps the `backups` records, the maintenance and `backup.*` settings and the
  restoring admin's sessions, and cancels jobs that were unfinished in the archive so it never starts generation or
  spend by itself. Schedules, the per-slot claim and the exclusive lease live under `backup.*` keys in
  `instance_settings`. `mangaveyra backup restore` refuses while a server announced itself in the last 90 seconds.
  Restore progress is held by the process running it (the jobs table is itself replaced). The member export is built in
  the request and kept for 7 days.
- **Import (§5) as built.** Up to 4096 entries, 256 pages (the chapter limit), 64 MB per entry and 2 GB in total;
  encrypted entries are refused; the RAR dictionary is capped at 64 MB. A failed import keeps its upload and Retry
  resumes from the last saved page. Detection runs only when an online worker offers it and local only mode is off.
- **Updates (§6) as built.** Update checks and settings live under `update.*` keys; an exclusive `update.lock` lease
  covers installs across processes. After the swap the server restarts in place when started by hand and exits with
  status 75 under systemd (the shipped unit restarts on it). If the new binary fails to start after passing the
  loopback health check, the record stays at "restarting" and `mangaveyra.prev` plus the backup (when kept) allow a manual
  rollback.

## Review follow-ups (2026-11-09)

- **Agents (§7).** Agent tokens are accepted only at `/mcp` (403 `agent_token_mcp_only` everywhere else, including the
  collaboration API, the WebSocket and doc:page); personal API tokens keep REST access. Approving a waiting generation
  runs it only within the estimate shown in G13 (1 %, at most one cent); a higher estimate keeps it waiting with the new
  estimate shown. Undoing an agent generation needs its job finished and no other page version since the job's first
  one ("Can't undo: edited since"). `mcp-stdio` sends the token only to the `--url` origin and refuses cross-origin
  redirects; G13 suggests `MV_AGENT_TOKEN` rather than `--token`.
- **Collaboration (§8).** `doc:page` carries presence and `collab.changed` only; it takes no Yjs document writes until an
  editor reads the document. Stored logs replay in batches and binary awareness frames are capped at 4 KiB.
- **Providers (§9).** ComfyUI and generic HTTP are server connections only an admin creates or edits; members use them
  through routing. Hugging Face stays a personal connection limited to Hugging Face's own HTTPS hosts. Hugging Face,
  ComfyUI and HTTP connections turn on, route and dispatch only while their last test passed for their current endpoint,
  key and description; local text connections turn on only after a passed test. A queued job whose route now costs more
  than its approved estimate fails as `approval_required` before any reservation.
- **Maintenance ownership (§3).** An update or a restore holds the server while it runs: maintenance mode on and owned by
  it (G5's toggle answers "An update is running" or "A restore is running" and changes nothing), the backup lease held,
  and no new job claims for the Go executor or workers (`jobs.Queue` claim gates). The previous setting is put back when
  it ends; a failed rollback restore leaves maintenance on.
- **Backups (§4), after review.** A restore keeps the `update.*` settings and the `updates` table and merges
  `audit_log`, `spend_ledger` and `spend_reservations` (the archive's rows plus every live row, live winning), so audit
  history and spend are never rolled back. Backups and restores act only on objects under `projects/`, `assets/`,
  `jobs/`, `imports/` or referenced by the database; a shared store's other keys are never archived, overwritten or
  deleted. Archives registered from elsewhere are never pruned, and deleting a record removes only a file the module
  wrote. Archives whose table of contents exceeds 400,000 entries or 48 MiB are refused before parsing. The member export
  reads one snapshot without the server-wide lease: one per member at a time, a new one at most hourly (the newest is
  served again in between), only the newest kept, and only media of the member's own projects. Scheduled backups wait
  up to 2 h for running work.
- **Import (§5), after review.** Zips are refused from their end record when they declare more than 4096 entries or a
  larger table of contents; PDFs render page by page and stop at the first page over a limit; an oversized image in a
  folder is skipped with a note; temporary files older than a day are removed.
- **Updates (§6), after review.** The install holds the server from before the pre-update backup until it ends: it waits
  up to 30 min for running backup work (logged), and its rollback restore runs under the hold, so no backup, export or
  maintenance change can stand in its way; the restarted server ends the hold. The 03:00 patch install waits while a
  backup runs or that hour's scheduled backup has not started. Downloads must send headers within 30 s, never pause over
  60 s and finish within 2 min plus the signed size at 64 KiB/s; a stalled download restores like any failure. Manifest
  versions are compared parsed (v1.0.2 = 1.0.2+build.7), and an update lease is taken over only while still expired.
